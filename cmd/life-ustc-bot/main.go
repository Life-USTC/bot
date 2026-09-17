package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/botapp"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/config"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/feedback"
	"github.com/Life-USTC/Bot/internal/health"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/napcat"
	"github.com/Life-USTC/Bot/internal/notify"
	"github.com/Life-USTC/Bot/internal/qqbot"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func main() {
	log.SetOutput(textutil.RedactingLogWriter(os.Stderr))
	cfg := config.FromEnv()
	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		if err := migrateDatabase(cfg.DBPath); err != nil {
			log.Printf("migration failed: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := health.Probe(ctx, cfg.HealthAddr); err != nil {
			log.Printf("healthcheck failed: %v", err)
			os.Exit(1)
		}
		return
	}
	logger := log.New(textutil.RedactingLogWriter(os.Stdout), "", log.LstdFlags)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	httpClient := &http.Client{
		Timeout:   cfg.HTTPClientTimeout,
		Transport: transport,
	}
	lifeClient := life.NewClient(cfg.LifeServer, httpClient)
	stateStore, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = stateStore.Close() }()
	if interrupted, err := stateStore.InterruptStartedAgentRuns(context.Background()); err != nil {
		logger.Fatalf("recover interrupted agent runs: %v", err)
	} else if interrupted > 0 {
		logger.Printf("Marked %d interrupted agent runs", interrupted)
	}
	renderer := responses.RemoteRenderer{Endpoint: cfg.RenderEndpoint, Client: httpClient}
	deliveryService, err := delivery.New(stateStore)
	if err != nil {
		logger.Fatalf("create delivery service: %v", err)
	}
	if err := deliveryService.SetRenderer(renderer); err != nil {
		logger.Fatalf("configure delivery renderer: %v", err)
	}
	publicCommandCache := commands.NewPublicCommandCache(
		stateStore,
		cfg.BuildVersion,
		cfg.PublicCommandCacheTTL,
		logger,
	)
	if err := publicCommandCache.Purge(context.Background()); err != nil {
		logger.Printf("purge public command cache: %v", err)
	}
	logger.Printf("Public command cache enabled: version=%s ttl=%s", cfg.BuildVersion, cfg.PublicCommandCacheTTL)
	platformsEnabled := false
	feedbackService, err := feedback.New(stateStore, feedback.Config{Targets: feedback.AdminTargets(
		cfg.FeedbackAdminPlatform,
		cfg.FeedbackAdminUsers,
		cfg.FeedbackAdminGroups,
	)})
	if err != nil {
		logger.Fatalf("create feedback service: %v", err)
	}
	authManager := &auth.Manager{
		Server:     cfg.LifeServer,
		HTTPClient: httpClient,
		Store:      stateStore,
	}
	var napcatBridge *napcat.Bridge
	handler := commands.Handler{
		Life:        lifeClient,
		Auth:        authManager,
		Store:       stateStore,
		Logger:      logger,
		Feedback:    feedbackService,
		PublicCache: publicCommandCache,
	}
	agentService, err := agent.New(context.Background(), agent.Config{
		Enabled:        cfg.EnableAgent,
		APIKey:         cfg.LLMAPIKey,
		BaseURL:        cfg.LLMBaseURL,
		Model:          cfg.LLMModel,
		PremiumAPIKey:  cfg.PremiumAPIKey,
		PremiumBaseURL: cfg.PremiumBaseURL,
		PremiumModel:   cfg.PremiumModel,

		AttachmentAPIKey:     cfg.AttachmentAPIKey,
		AttachmentBaseURL:    cfg.AttachmentBaseURL,
		AttachmentLocalPaths: cfg.AttachmentLocalPaths,
		Logger:               logger,
		MCPBaseURL:           strings.TrimRight(cfg.LifeServer, "/") + "/api/mcp/",
		AuthManager:          authManager,
	}, handler, httpClient)
	if err != nil {
		logger.Fatalf("create agent service: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	healthServer := &http.Server{Addr: cfg.HealthAddr, Handler: health.NewHandler(stateStore)}
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("health server stopped: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = healthServer.Shutdown(shutdownCtx)
	}()
	logger.Printf("Health server listening on %s", cfg.HealthAddr)
	app, err := botapp.NewCoordinator(botapp.CoordinatorConfig{
		Jobs:     stateStore,
		Commands: handler,
		Agent:    agentService,
		Outputs:  deliveryService,
		Replies:  stateStore,
		Recorder: stateStore,
		Logger:   logger,
	})
	if err != nil {
		logger.Fatalf("create bot application: %v", err)
	}
	go app.Run(ctx)
	logger.Printf("Conversation job coordinator started")

	if cfg.EnableNapCatBridge && cfg.NapCatWSURL != "" {
		napcatBridge = &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			WSURL:       cfg.NapCatWSURL,
			App:         app,
			HTTPClient:  httpClient,
			Logger:      logger,

			AllowLocalMediaPaths: cfg.AttachmentLocalPaths,
		}
		if err := deliveryService.Register(napcat.NewDeliveryAdapter(napcatBridge)); err != nil {
			logger.Fatalf("register NapCat delivery adapter: %v", err)
		}
		platformsEnabled = true
		go func() {
			if err := napcatBridge.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Fatalf("NapCat bridge stopped: %v", err)
			}
		}()
		logger.Printf("NapCat bridge connecting to %s", cfg.NapCatWSURL)
	} else if cfg.EnableNapCatBridge {
		napcatBridge = &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			App:         app,
			HTTPClient:  httpClient,
			Logger:      logger,

			AllowLocalMediaPaths: cfg.AttachmentLocalPaths,
		}
		if err := deliveryService.Register(napcat.NewDeliveryAdapter(napcatBridge)); err != nil {
			logger.Fatalf("register NapCat delivery adapter: %v", err)
		}
		platformsEnabled = true
		go func() {
			if err := napcatBridge.RunReverse(ctx, cfg.NapCatReverseAddr, cfg.NapCatReversePath); err != nil && ctx.Err() == nil {
				logger.Fatalf("NapCat reverse bridge stopped: %v", err)
			}
		}()
		logger.Printf("NapCat reverse bridge listening on %s%s", cfg.NapCatReverseAddr, cfg.NapCatReversePath)
	}
	if cfg.EnableQQBot {
		qqBot := &qqbot.Bot{
			AppID:      cfg.QQBotAppID,
			AppSecret:  cfg.QQBotAppSecret,
			BotToken:   cfg.QQBotToken,
			BotID:      cfg.QQBotID,
			APIBaseURL: cfg.QQBotAPIBaseURL,
			TokenURL:   cfg.QQBotTokenURL,
			GatewayURL: cfg.QQBotGatewayURL,
			Intents:    cfg.QQBotIntents,
			App:        app,
			HTTPClient: httpClient,
			Logger:     logger,
		}
		if err := deliveryService.Register(qqbot.NewDeliveryAdapter(qqBot)); err != nil {
			logger.Fatalf("register QQ delivery adapter: %v", err)
		}
		platformsEnabled = true
		if cfg.EnableQQBotWebhook {
			go func() {
				if err := qqBot.RunWebhook(ctx, cfg.QQBotWebhookAddr, cfg.QQBotWebhookPath); err != nil && ctx.Err() == nil {
					logger.Fatalf("QQ official bot webhook stopped: %v", err)
				}
			}()
			logger.Printf("QQ official bot webhook listening on %s%s", cfg.QQBotWebhookAddr, cfg.QQBotWebhookPath)
		}
		if cfg.EnableQQBotGateway {
			go func() {
				if err := qqBot.Run(ctx); err != nil && ctx.Err() == nil {
					logger.Fatalf("QQ official bot gateway stopped: %v", err)
				}
			}()
			logger.Printf("QQ official bot gateway enabled")
		}
	}

	if platformsEnabled {
		deliveryWorker := &delivery.Worker{Service: deliveryService, Logger: logger}
		go deliveryWorker.Run(ctx)
		logger.Printf("Delivery worker started")
		loginPoller := &auth.LoginPoller{
			Manager: authManager,
			Logger:  logger,
		}
		go loginPoller.Run(ctx)
		logger.Printf("Login poller started")
		notificationPoller := &notify.Poller{
			Life:      lifeClient,
			Auth:      authManager,
			Store:     stateStore,
			Publisher: deliveryService,
			Logger:    logger,
		}
		go notificationPoller.Run(ctx)
		logger.Printf("Notification poller started")
	}

	<-ctx.Done()
}

func migrateDatabase(path string) error {
	stateStore, err := store.OpenForSchemaMaintenance(path)
	if err != nil {
		return err
	}
	defer func() { _ = stateStore.Close() }()
	return stateStore.PrepareSchemaForMaintenance(context.Background())
}
