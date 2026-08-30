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
)

func main() {
	cfg := config.FromEnv()
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := health.Probe(ctx, cfg.HealthAddr); err != nil {
			log.Printf("healthcheck failed: %v", err)
			os.Exit(1)
		}
		return
	}
	logger := log.New(os.Stdout, "", log.LstdFlags)
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
	deliveryService, err := delivery.New(stateStore)
	if err != nil {
		logger.Fatalf("create delivery service: %v", err)
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
	var mediaStore *responses.MediaStore
	renderer := responses.Renderer{FontPath: cfg.ImageFontPath}
	if cfg.EnableImageResponses && cfg.PublicBaseURL != "" {
		mediaStore = responses.NewMediaStore(strings.TrimRight(cfg.PublicBaseURL, "/")+"/media", cfg.MediaTTL)
		mediaServer := &http.Server{Addr: cfg.MediaAddr, Handler: mediaStore}
		go func() {
			if err := mediaServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Printf("media server stopped: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = mediaServer.Shutdown(shutdownCtx)
		}()
		logger.Printf("Media server listening on %s", cfg.MediaAddr)
	} else if cfg.EnableImageResponses {
		logger.Printf("Image responses disabled: BOT_PUBLIC_BASE_URL is empty")
	}
	var napcatBridge *napcat.Bridge
	handler := commands.Handler{
		Life:                   lifeClient,
		Auth:                   authManager,
		Store:                  stateStore,
		Logger:                 logger,
		Feedback:               feedbackService,
		AllowGroupPersonalInfo: cfg.AllowGroupPersonalInfo,
		EnableImageResponses:   cfg.EnableImageResponses && mediaStore != nil,
		PublicCache:            publicCommandCache,
	}
	agentService, err := agent.New(context.Background(), agent.Config{
		Enabled:        cfg.EnableAgent,
		APIKey:         cfg.LLMAPIKey,
		BaseURL:        cfg.LLMBaseURL,
		Model:          cfg.LLMModel,
		Timeout:        cfg.LLMTimeout,
		PremiumAPIKey:  cfg.PremiumAPIKey,
		PremiumBaseURL: cfg.PremiumBaseURL,
		PremiumModel:   cfg.PremiumModel,
		Logger:         logger,
		MCPBaseURL:     strings.TrimRight(cfg.LifeServer, "/") + "/api/mcp/",
		AuthManager:    authManager,
		Feedback:       feedbackService,
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
	var agentDispatcher *agent.Dispatcher
	if agentService.Enabled() {
		agentDispatcher = agent.NewDispatcher(ctx, agentService, agent.DispatcherConfig{Logger: logger})
	}
	app, err := botapp.New(botapp.Config{
		Commands:   handler,
		Agent:      agentService,
		Dispatcher: agentDispatcher,
		Delivery:   deliveryService,
		Recorder:   stateStore,
		Renderer:   renderer,
		Logger:     logger,
	})
	if err != nil {
		logger.Fatalf("create bot application: %v", err)
	}

	if cfg.EnableNapCatBridge && cfg.NapCatWSURL != "" {
		napcatBridge = &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			WSURL:       cfg.NapCatWSURL,
			App:         app,
			HTTPClient:  httpClient,
			Logger:      logger,
			MediaStore:  mediaStore,
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
			MediaStore:  mediaStore,
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
			MediaStore: mediaStore,
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
			Resume:  app.ResumePendingRequest,
		}
		go loginPoller.Run(ctx)
		logger.Printf("Login poller started")
		notificationPoller := &notify.Poller{
			Life:                 lifeClient,
			Auth:                 authManager,
			Store:                stateStore,
			Publisher:            deliveryService,
			Renderer:             renderer,
			Logger:               logger,
			EnableImageResponses: handler.EnableImageResponses,
		}
		go notificationPoller.Run(ctx)
		logger.Printf("Notification poller started")
	}

	<-ctx.Done()
}
