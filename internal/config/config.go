package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LifeServer         string
	OneBotHTTPHost     string
	OneBotHTTPPort     uint16
	OneBotAccessToken  string
	OneBotSelfID       string
	NapCatAPIURL       string
	NapCatAccessToken  string
	NapCatWSURL        string
	NapCatReverseAddr  string
	NapCatReversePath  string
	CommandPrefix      string
	HTTPClientTimeout  time.Duration
	EnableOneBotServer bool
	EnableNapCatBridge bool
}

func FromEnv() Config {
	return Config{
		LifeServer:         envString("LIFE_USTC_SERVER", "http://localhost:3000"),
		OneBotHTTPHost:     envString("BOT_ONEBOT_HTTP_HOST", "127.0.0.1"),
		OneBotHTTPPort:     uint16(envInt("BOT_ONEBOT_HTTP_PORT", 6700)),
		OneBotAccessToken:  os.Getenv("BOT_ONEBOT_ACCESS_TOKEN"),
		OneBotSelfID:       envString("BOT_SELF_ID", "life-ustc"),
		NapCatAPIURL:       strings.TrimRight(envString("NAPCAT_API_URL", "http://127.0.0.1:3000"), "/"),
		NapCatAccessToken:  os.Getenv("NAPCAT_ACCESS_TOKEN"),
		NapCatWSURL:        os.Getenv("NAPCAT_WS_URL"),
		NapCatReverseAddr:  envString("NAPCAT_REVERSE_ADDR", "0.0.0.0:2280"),
		NapCatReversePath:  envString("NAPCAT_REVERSE_PATH", "/ws"),
		CommandPrefix:      envString("BOT_COMMAND_PREFIX", "/life"),
		HTTPClientTimeout:  time.Duration(envInt("BOT_HTTP_TIMEOUT_SECONDS", 15)) * time.Second,
		EnableOneBotServer: envBool("BOT_ENABLE_ONEBOT_SERVER", true),
		EnableNapCatBridge: envBool("BOT_ENABLE_NAPCAT_BRIDGE", true),
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
