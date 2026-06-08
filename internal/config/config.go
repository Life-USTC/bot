package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LifeServer          string
	OneBotHTTPHost      string
	OneBotHTTPPort      uint16
	OneBotAccessToken   string
	OneBotSelfID        string
	NapCatAPIURL        string
	NapCatAccessToken   string
	NapCatWSURL         string
	NapCatReverseAddr   string
	NapCatReversePath   string
	DBPath              string
	CommandPrefix       string
	HTTPClientTimeout   time.Duration
	EnableOneBotServer  bool
	EnableNapCatBridge  bool
	EnableAgent         bool
	LLMAPIKey           string
	LLMBaseURL          string
	LLMModel            string
	FeedbackAdminUsers  []string
	FeedbackAdminGroups []string
}

func FromEnv() Config {
	return Config{
		LifeServer:          envTrimRight("LIFE_USTC_SERVER", "http://localhost:3000", "/"),
		OneBotHTTPHost:      envString("BOT_ONEBOT_HTTP_HOST", "127.0.0.1"),
		OneBotHTTPPort:      envUint16("BOT_ONEBOT_HTTP_PORT", 6700),
		OneBotAccessToken:   envOptionalString("BOT_ONEBOT_ACCESS_TOKEN"),
		OneBotSelfID:        envString("BOT_SELF_ID", "life-ustc"),
		NapCatAPIURL:        envTrimRight("NAPCAT_API_URL", "http://127.0.0.1:3000", "/"),
		NapCatAccessToken:   envOptionalString("NAPCAT_ACCESS_TOKEN"),
		NapCatWSURL:         envOptionalString("NAPCAT_WS_URL"),
		NapCatReverseAddr:   envString("NAPCAT_REVERSE_ADDR", "0.0.0.0:2280"),
		NapCatReversePath:   envPath("NAPCAT_REVERSE_PATH", "/ws"),
		DBPath:              envString("BOT_DB_PATH", ".run/life-ustc-bot.db"),
		CommandPrefix:       envString("BOT_COMMAND_PREFIX", "/life"),
		HTTPClientTimeout:   time.Duration(envPositiveInt("BOT_HTTP_TIMEOUT_SECONDS", 15)) * time.Second,
		EnableOneBotServer:  envBool("BOT_ENABLE_ONEBOT_SERVER", true),
		EnableNapCatBridge:  envBool("BOT_ENABLE_NAPCAT_BRIDGE", true),
		EnableAgent:         envBool("BOT_ENABLE_AGENT", false),
		LLMAPIKey:           envOptionalString("OPENAI_API_KEY"),
		LLMBaseURL:          envOptionalString("OPENAI_BASE_URL"),
		LLMModel:            envString("BOT_LLM_MODEL", "gpt-4o-mini"),
		FeedbackAdminUsers:  envList("BOT_FEEDBACK_ADMIN_USERS"),
		FeedbackAdminGroups: envList("BOT_FEEDBACK_ADMIN_GROUPS"),
	}
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

func envUint16(key string, fallback uint16) uint16 {
	value, ok := envParsedInt(key)
	if !ok || value <= 0 || value > int(^uint16(0)) {
		return fallback
	}
	return uint16(value)
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
