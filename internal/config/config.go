package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LifeServer            string
	HealthAddr            string
	NapCatAPIURL          string
	NapCatAccessToken     string
	NapCatWSURL           string
	NapCatReverseAddr     string
	NapCatReversePath     string
	QQBotAppID            string
	QQBotAppSecret        string
	QQBotToken            string
	QQBotID               string
	QQBotAPIBaseURL       string
	QQBotTokenURL         string
	QQBotGatewayURL       string
	QQBotWebhookAddr      string
	QQBotWebhookPath      string
	QQBotIntents          uint64
	DBPath                string
	BuildVersion          string
	PublicCommandCacheTTL time.Duration
	HTTPClientTimeout     time.Duration
	EnableNapCatBridge    bool
	EnableQQBot           bool
	EnableQQBotGateway    bool
	EnableQQBotWebhook    bool
	EnableAgent           bool
	RenderEndpoint        string
	LLMAPIKey             string
	LLMBaseURL            string
	LLMModel              string
	PremiumAPIKey         string
	PremiumBaseURL        string
	PremiumModel          string
	AttachmentAPIKey      string
	AttachmentBaseURL     string
	AttachmentLocalPaths  bool
	FeedbackAdminPlatform string
	FeedbackAdminUsers    []string
	FeedbackAdminGroups   []string
}

func FromEnv() Config {
	cfg := Config{
		LifeServer:            envTrimRight("LIFE_USTC_SERVER", "http://localhost:3000", "/"),
		HealthAddr:            envString("BOT_HEALTH_ADDR", "127.0.0.1:2282"),
		NapCatAPIURL:          envTrimRight("NAPCAT_API_URL", "", "/"),
		NapCatAccessToken:     envOptionalString("NAPCAT_ACCESS_TOKEN"),
		NapCatWSURL:           envOptionalString("NAPCAT_WS_URL"),
		NapCatReverseAddr:     envString("NAPCAT_REVERSE_ADDR", "0.0.0.0:2280"),
		NapCatReversePath:     envPath("NAPCAT_REVERSE_PATH", "/ws"),
		QQBotAppID:            envOptionalString("QQ_BOT_APPID"),
		QQBotAppSecret:        envOptionalString("QQ_BOT_APPSECRET"),
		QQBotToken:            envOptionalString("QQ_BOT_TOKEN"),
		QQBotID:               envOptionalString("QQ_BOT_ID"),
		QQBotAPIBaseURL:       envTrimRight("QQ_BOT_API_BASE_URL", "https://api.sgroup.qq.com", "/"),
		QQBotTokenURL:         envString("QQ_BOT_TOKEN_URL", "https://bots.qq.com/app/getAppAccessToken"),
		QQBotGatewayURL:       envOptionalString("QQ_BOT_GATEWAY_URL"),
		QQBotWebhookAddr:      envString("QQ_BOT_WEBHOOK_ADDR", "0.0.0.0:2290"),
		QQBotWebhookPath:      envPath("QQ_BOT_WEBHOOK_PATH", "/qqbot"),
		QQBotIntents:          envUint64("QQ_BOT_INTENTS", 1<<12|1<<25|1<<26|1<<30),
		DBPath:                envString("BOT_DB_PATH", ".run/life-ustc-bot.db"),
		BuildVersion:          envString("BOT_BUILD_VERSION", "dev"),
		PublicCommandCacheTTL: time.Duration(envPositiveInt("BOT_PUBLIC_COMMAND_CACHE_TTL_SECONDS", 300)) * time.Second,
		HTTPClientTimeout:     time.Duration(envPositiveInt("BOT_HTTP_TIMEOUT_SECONDS", 60)) * time.Second,
		EnableNapCatBridge:    envBool("BOT_ENABLE_NAPCAT_BRIDGE", true),
		EnableQQBot:           envBool("BOT_ENABLE_QQ_BOT", hasQQBotCredentials()),
		EnableQQBotGateway:    envBool("BOT_ENABLE_QQ_BOT_GATEWAY", true),
		EnableQQBotWebhook:    envBool("BOT_ENABLE_QQ_BOT_WEBHOOK", hasQQBotWebhookCredentials()),
		EnableAgent:           envBool("BOT_ENABLE_AGENT", false),
		RenderEndpoint:        envString("BOT_RENDER_ENDPOINT", "http://127.0.0.1:9123/render"),
		LLMAPIKey:             envOptionalString("OPENAI_API_KEY"),
		LLMBaseURL:            envOptionalString("OPENAI_BASE_URL"),
		LLMModel:              envString("BOT_LLM_MODEL", "gpt-4o-mini"),
		PremiumAPIKey:         envOptionalString("PREMIUM_MODEL_API_KEY"),
		PremiumBaseURL:        envTrimRight("PREMIUM_MODEL_BASE_URL", "https://api.moonshot.cn/v1", "/"),
		PremiumModel:          envString("PREMIUM_MODEL", "kimi-k3"),
		AttachmentAPIKey:      envOptionalString("KIMI_FILE_API_KEY"),
		AttachmentBaseURL:     envTrimRight("KIMI_FILE_BASE_URL", "", "/"),
		AttachmentLocalPaths:  envBool("BOT_ATTACHMENT_LOCAL_PATHS", false),
		FeedbackAdminPlatform: envOptionalString("BOT_FEEDBACK_ADMIN_PLATFORM"),
		FeedbackAdminUsers:    envList("BOT_FEEDBACK_ADMIN_USERS"),
		FeedbackAdminGroups:   envList("BOT_FEEDBACK_ADMIN_GROUPS"),
	}
	// File extraction and the chat model are separate endpoints: a chat model
	// can live on a host without a file-extract API. Attachment credentials
	// therefore default to the premium pair, so a deployment that sets only
	// PREMIUM_MODEL_* keeps its previous behaviour.
	if cfg.AttachmentAPIKey == "" {
		cfg.AttachmentAPIKey = cfg.PremiumAPIKey
	}
	if cfg.AttachmentBaseURL == "" {
		cfg.AttachmentBaseURL = cfg.PremiumBaseURL
	}
	return cfg
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envOptionalString(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envTrimRight(key, fallback, cutset string) string {
	value := strings.TrimRight(envString(key, fallback), cutset)
	if value == "" {
		return strings.TrimRight(fallback, cutset)
	}
	return value
}

func envPath(key, fallback string) string {
	value := envString(key, fallback)
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func envInt(key string, fallback int) int {
	value, ok := envParsedInt(key)
	if !ok {
		return fallback
	}
	return value
}

func envPositiveInt(key string, fallback int) int {
	value := envInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func envUint64(key string, fallback uint64) uint64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return fallback
	}
	return value
}

func envParsedInt(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
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

func hasQQBotCredentials() bool {
	appID := envOptionalString("QQ_BOT_APPID")
	appSecret := envOptionalString("QQ_BOT_APPSECRET")
	token := envOptionalString("QQ_BOT_TOKEN")
	return appID != "" && (appSecret != "" || token != "")
}

func hasQQBotWebhookCredentials() bool {
	return envOptionalString("QQ_BOT_APPID") != "" && envOptionalString("QQ_BOT_APPSECRET") != ""
}
