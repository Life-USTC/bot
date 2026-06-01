package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/config"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/napcat"
	"github.com/Life-USTC/Bot/internal/onebot12"
	"github.com/Life-USTC/Bot/internal/store"
)

func main() {
	cfg := config.FromEnv()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	httpClient := &http.Client{Timeout: cfg.HTTPClientTimeout}
	lifeClient := life.NewClient(cfg.LifeServer, httpClient)
	stateStore, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = stateStore.Close() }()
	authManager := &auth.Manager{
		Server:     cfg.LifeServer,
		HTTPClient: httpClient,
		Store:      stateStore,
	}
	handler := commands.Handler{Life: lifeClient, Auth: authManager, Store: stateStore, Prefix: cfg.CommandPrefix}

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
		bridge := &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			WSURL:       cfg.NapCatWSURL,
			Handler:     handler,
			HTTPClient:  httpClient,
			Logger:      logger,
		}
		go func() {
			if err := bridge.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Printf("NapCat bridge stopped: %v", err)
			}
		}()
		logger.Printf("NapCat bridge connecting to %s", cfg.NapCatWSURL)
	} else if cfg.EnableNapCatBridge {
		bridge := &napcat.Bridge{
			APIURL:      cfg.NapCatAPIURL,
			AccessToken: cfg.NapCatAccessToken,
			Handler:     handler,
			HTTPClient:  httpClient,
			Logger:      logger,
		}
		go func() {
			if err := bridge.RunReverse(ctx, cfg.NapCatReverseAddr, cfg.NapCatReversePath); err != nil && ctx.Err() == nil {
				logger.Printf("NapCat reverse bridge stopped: %v", err)
			}
		}()
		logger.Printf("NapCat reverse bridge listening on %s%s", cfg.NapCatReverseAddr, cfg.NapCatReversePath)
	}

	<-ctx.Done()
}
