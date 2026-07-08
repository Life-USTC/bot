package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/config"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/napcat"
	"github.com/Life-USTC/Bot/internal/notify"
	"github.com/Life-USTC/Bot/internal/onebot12"
	"github.com/Life-USTC/Bot/internal/qqbot"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type messageSender interface {
	SendMessage(ctx context.Context, ident store.Identity, message string) error
	SendLoginMessage(ctx context.Context, ident store.Identity, message string) error
}

type platformSender struct {
	platform string
	sender   messageSender
}

type senderRouter struct {
	senders []platformSender
}

func (r *senderRouter) Add(platform string, sender messageSender) {
	if sender == nil {
		return
	}
	r.senders = append(r.senders, platformSender{platform: strings.ToLower(strings.TrimSpace(platform)), sender: sender})
}

func (r *senderRouter) SendMessage(ctx context.Context, ident store.Identity, message string) error {
	return r.send(ctx, ident, message, false)
}

func (r *senderRouter) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	return r.send(ctx, ident, message, true)
}

func (r *senderRouter) send(ctx context.Context, ident store.Identity, message string, login bool) error {
	platform := strings.ToLower(strings.TrimSpace(ident.Platform))
	for _, item := range r.senders {
		if item.platform != platform {
			continue
		}
		if login {
			return item.sender.SendLoginMessage(ctx, ident, message)
		}
		return item.sender.SendMessage(ctx, ident, message)
	}
	if len(r.senders) == 1 {
		if login {
			return r.senders[0].sender.SendLoginMessage(ctx, ident, message)
		}
		return r.senders[0].sender.SendMessage(ctx, ident, message)
	}
	return fmt.Errorf("no message sender configured for platform %q", ident.Platform)
}

func (r *senderRouter) Available() bool {
	return len(r.senders) > 0
}

func main() {
	cfg := config.FromEnv()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	httpClient := &http.Client{
		Timeout: cfg.HTTPClientTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	lifeClient := life.NewClient(cfg.LifeServer, httpClient)
	stateStore, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = stateStore.Close() }()
	messageRouter := &senderRouter{}
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
		Prefix:                 cfg.CommandPrefix,
		Logger:                 logger,
		FeedbackUsers:          cfg.FeedbackAdminUsers,
		FeedbackGroups:         cfg.FeedbackAdminGroups,
		FeedbackSend:           messageRouter.SendMessage,
		AllowGroupPersonalInfo: cfg.AllowGroupPersonalInfo,
		EnableImageResponses:   cfg.EnableImageResponses && mediaStore != nil,
	}
	agentService, err := agent.New(context.Background(), agent.Config{
		Enabled:     cfg.EnableAgent,
		APIKey:      cfg.LLMAPIKey,
		BaseURL:     cfg.LLMBaseURL,
		Model:       cfg.LLMModel,
		Timeout:     cfg.LLMTimeout,
		Logger:      logger,
		MCPBaseURL:  strings.TrimRight(cfg.LifeServer, "/") + "/api/mcp/",
		AuthManager: authManager,
	}, handler, httpClient)
	if err != nil {
		logger.Fatalf("create agent service: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.EnableOneBotServer {
		server := onebot12.New(onebot12.Config{
			Host:        cfg.OneBotHTTPHost,
			Port:        cfg.OneBotHTTPPort,
			AccessToken: cfg.OneBotAccessToken,
			SelfID:      cfg.OneBotSelfID,
			Auth:        authManager,
		}, lifeClient)
		go server.Run()
		defer server.Shutdown()
		logger.Printf("OneBot 12 HTTP server listening on %s:%d", cfg.OneBotHTTPHost, cfg.OneBotHTTPPort)
	}

	if cfg.EnableNapCatBridge && cfg.NapCatWSURL != "" {
		napcatBridge = &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			WSURL:       cfg.NapCatWSURL,
			Handler:     handler,
			Agent:       agentService,
			HTTPClient:  httpClient,
			Logger:      logger,
			Renderer:    renderer,
			MediaStore:  mediaStore,
		}
		messageRouter.Add("napcat", napcatBridge)
		go func() {
			if err := napcatBridge.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Printf("NapCat bridge stopped: %v", err)
			}
		}()
		logger.Printf("NapCat bridge connecting to %s", cfg.NapCatWSURL)
	} else if cfg.EnableNapCatBridge {
		napcatBridge = &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			Handler:     handler,
			Agent:       agentService,
			HTTPClient:  httpClient,
			Logger:      logger,
			Renderer:    renderer,
			MediaStore:  mediaStore,
		}
		messageRouter.Add("napcat", napcatBridge)
		go func() {
			if err := napcatBridge.RunReverse(ctx, cfg.NapCatReverseAddr, cfg.NapCatReversePath); err != nil && ctx.Err() == nil {
				logger.Printf("NapCat reverse bridge stopped: %v", err)
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
			Handler:    handler,
			Agent:      agentService,
			HTTPClient: httpClient,
			Logger:     logger,
			Renderer:   renderer,
			MediaStore: mediaStore,
		}
		messageRouter.Add("qqbot", qqBot)
		if cfg.EnableQQBotWebhook {
			go func() {
				if err := qqBot.RunWebhook(ctx, cfg.QQBotWebhookAddr, cfg.QQBotWebhookPath); err != nil && ctx.Err() == nil {
					logger.Printf("QQ official bot webhook stopped: %v", err)
				}
			}()
			logger.Printf("QQ official bot webhook listening on %s%s", cfg.QQBotWebhookAddr, cfg.QQBotWebhookPath)
		}
		if cfg.EnableQQBotGateway {
			go func() {
				if err := qqBot.Run(ctx); err != nil && ctx.Err() == nil {
					logger.Printf("QQ official bot gateway stopped: %v", err)
				}
			}()
			logger.Printf("QQ official bot gateway enabled")
		}
	}
	if messageRouter.Available() {
		loginPoller := &auth.LoginPoller{
			Manager:  authManager,
			Notifier: messageRouter,
			Logger:   logger,
		}
		go loginPoller.Run(ctx)
		logger.Printf("Login poller started")
		notificationPoller := &notify.Poller{
			Life:   lifeClient,
			Auth:   authManager,
			Store:  stateStore,
			Sender: messageRouter,
			Logger: logger,
		}
		go notificationPoller.Run(ctx)
		logger.Printf("Notification poller started")
	}

	<-ctx.Done()
}
