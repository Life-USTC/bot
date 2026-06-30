package config

import (
	"strings"
	"testing"
	"time"
)

func TestFromEnvParsesOneBotHTTPPort(t *testing.T) {
	t.Setenv("BOT_ONEBOT_HTTP_PORT", "8080")

	cfg := FromEnv()
	if cfg.OneBotHTTPPort != 8080 {
		t.Fatalf("OneBotHTTPPort = %d, want 8080", cfg.OneBotHTTPPort)
	}
}

func TestFromEnvFallsBackForInvalidOneBotHTTPPort(t *testing.T) {
	for _, value := range []string{"bad", "0", "-1", "70000"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BOT_ONEBOT_HTTP_PORT", value)

			cfg := FromEnv()
			if cfg.OneBotHTTPPort != 6700 {
				t.Fatalf("OneBotHTTPPort = %d, want 6700", cfg.OneBotHTTPPort)
			}
		})
	}
}

func TestFromEnvParsesHTTPClientTimeout(t *testing.T) {
	t.Setenv("BOT_HTTP_TIMEOUT_SECONDS", "30")

	cfg := FromEnv()
	if cfg.HTTPClientTimeout != 30*time.Second {
		t.Fatalf("HTTPClientTimeout = %s, want 30s", cfg.HTTPClientTimeout)
	}
}

func TestFromEnvFallsBackForInvalidHTTPClientTimeout(t *testing.T) {
	for _, value := range []string{"bad", "0", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BOT_HTTP_TIMEOUT_SECONDS", value)

			cfg := FromEnv()
			if cfg.HTTPClientTimeout != 60*time.Second {
				t.Fatalf("HTTPClientTimeout = %s, want 60s", cfg.HTTPClientTimeout)
			}
		})
	}
}

func TestFromEnvTrimsOptionalStrings(t *testing.T) {
	t.Setenv("BOT_ONEBOT_ACCESS_TOKEN", " onebot-token ")
	t.Setenv("NAPCAT_ACCESS_TOKEN", " napcat-token ")
	t.Setenv("NAPCAT_WS_URL", " ws://127.0.0.1:3001 ")
	t.Setenv("QQ_BOT_APPID", " appid ")
	t.Setenv("QQ_BOT_APPSECRET", " secret ")
	t.Setenv("QQ_BOT_TOKEN", " qq-token ")
	t.Setenv("QQ_BOT_ID", " bot-id ")
	t.Setenv("QQ_BOT_GATEWAY_URL", " wss://gateway.example/ws ")
	t.Setenv("OPENAI_API_KEY", " api-key ")
	t.Setenv("OPENAI_BASE_URL", " https://llm.example/v1 ")
	t.Setenv("BOT_LLM_TIMEOUT_SECONDS", "120")

	cfg := FromEnv()
	if cfg.OneBotAccessToken != "onebot-token" {
		t.Fatalf("OneBotAccessToken = %q", cfg.OneBotAccessToken)
	}
	if cfg.NapCatAccessToken != "napcat-token" {
		t.Fatalf("NapCatAccessToken = %q", cfg.NapCatAccessToken)
	}
	if cfg.NapCatWSURL != "ws://127.0.0.1:3001" {
		t.Fatalf("NapCatWSURL = %q", cfg.NapCatWSURL)
	}
	if cfg.QQBotAppID != "appid" {
		t.Fatalf("QQBotAppID = %q", cfg.QQBotAppID)
	}
	if cfg.QQBotAppSecret != "secret" {
		t.Fatalf("QQBotAppSecret = %q", cfg.QQBotAppSecret)
	}
	if cfg.QQBotToken != "qq-token" {
		t.Fatalf("QQBotToken = %q", cfg.QQBotToken)
	}
	if cfg.QQBotID != "bot-id" {
		t.Fatalf("QQBotID = %q", cfg.QQBotID)
	}
	if cfg.QQBotGatewayURL != "wss://gateway.example/ws" {
		t.Fatalf("QQBotGatewayURL = %q", cfg.QQBotGatewayURL)
	}
	if cfg.LLMAPIKey != "api-key" {
		t.Fatalf("LLMAPIKey = %q", cfg.LLMAPIKey)
	}
	if cfg.LLMBaseURL != "https://llm.example/v1" {
		t.Fatalf("LLMBaseURL = %q", cfg.LLMBaseURL)
	}
	if cfg.LLMTimeout != 120*time.Second {
		t.Fatalf("LLMTimeout = %s", cfg.LLMTimeout)
	}
}

func TestFromEnvParsesFeedbackTargets(t *testing.T) {
	t.Setenv("BOT_FEEDBACK_ADMIN_USERS", " 42, 43;44 ")
	t.Setenv("BOT_FEEDBACK_ADMIN_GROUPS", " 100 101 ")

	cfg := FromEnv()
	if strings.Join(cfg.FeedbackAdminUsers, ",") != "42,43,44" {
		t.Fatalf("FeedbackAdminUsers = %#v", cfg.FeedbackAdminUsers)
	}
	if strings.Join(cfg.FeedbackAdminGroups, ",") != "100,101" {
		t.Fatalf("FeedbackAdminGroups = %#v", cfg.FeedbackAdminGroups)
	}
}

func TestFromEnvParsesAllowGroupPersonalInfo(t *testing.T) {
	t.Setenv("BOT_ALLOW_GROUP_PERSONAL_INFO", "true")

	cfg := FromEnv()
	if !cfg.AllowGroupPersonalInfo {
		t.Fatal("AllowGroupPersonalInfo = false, want true")
	}
}

func TestFromEnvTrimsNapCatAPIURLTrailingSlash(t *testing.T) {
	t.Setenv("NAPCAT_API_URL", " http://127.0.0.1:3000/// ")

	cfg := FromEnv()
	if cfg.NapCatAPIURL != "http://127.0.0.1:3000" {
		t.Fatalf("NapCatAPIURL = %q", cfg.NapCatAPIURL)
	}
}

func TestFromEnvTrimsLifeServerTrailingSlash(t *testing.T) {
	t.Setenv("LIFE_USTC_SERVER", " http://life.example/// ")

	cfg := FromEnv()
	if cfg.LifeServer != "http://life.example" {
		t.Fatalf("LifeServer = %q", cfg.LifeServer)
	}
}

func TestFromEnvFallsBackForEmptyLifeServerAfterTrim(t *testing.T) {
	t.Setenv("LIFE_USTC_SERVER", " / ")

	cfg := FromEnv()
	if cfg.LifeServer != "http://localhost:3000" {
		t.Fatalf("LifeServer = %q", cfg.LifeServer)
	}
}

func TestFromEnvFallsBackForEmptyNapCatAPIURLAfterTrim(t *testing.T) {
	t.Setenv("NAPCAT_API_URL", " / ")

	cfg := FromEnv()
	if cfg.NapCatAPIURL != "http://127.0.0.1:3000" {
		t.Fatalf("NapCatAPIURL = %q", cfg.NapCatAPIURL)
	}
}

func TestFromEnvParsesQQBotConfig(t *testing.T) {
	t.Setenv("QQ_BOT_APPID", "appid")
	t.Setenv("QQ_BOT_APPSECRET", "secret")
	t.Setenv("QQ_BOT_API_BASE_URL", " https://sandbox.api.sgroup.qq.com/// ")
	t.Setenv("QQ_BOT_TOKEN_URL", " https://bots.qq.com/token ")
	t.Setenv("QQ_BOT_WEBHOOK_ADDR", " 127.0.0.1:9999 ")
	t.Setenv("QQ_BOT_WEBHOOK_PATH", " callback ")
	t.Setenv("QQ_BOT_INTENTS", "33554432")

	cfg := FromEnv()
	if !cfg.EnableQQBot {
		t.Fatal("EnableQQBot = false, want true with credentials")
	}
	if !cfg.EnableQQBotGateway {
		t.Fatal("EnableQQBotGateway = false, want true by default")
	}
	if !cfg.EnableQQBotWebhook {
		t.Fatal("EnableQQBotWebhook = false, want true with app secret")
	}
	if cfg.QQBotAPIBaseURL != "https://sandbox.api.sgroup.qq.com" {
		t.Fatalf("QQBotAPIBaseURL = %q", cfg.QQBotAPIBaseURL)
	}
	if cfg.QQBotTokenURL != "https://bots.qq.com/token" {
		t.Fatalf("QQBotTokenURL = %q", cfg.QQBotTokenURL)
	}
	if cfg.QQBotWebhookAddr != "127.0.0.1:9999" {
		t.Fatalf("QQBotWebhookAddr = %q", cfg.QQBotWebhookAddr)
	}
	if cfg.QQBotWebhookPath != "/callback" {
		t.Fatalf("QQBotWebhookPath = %q", cfg.QQBotWebhookPath)
	}
	if cfg.QQBotIntents != 33554432 {
		t.Fatalf("QQBotIntents = %d", cfg.QQBotIntents)
	}
}

func TestFromEnvQQBotCanBeDisabled(t *testing.T) {
	t.Setenv("QQ_BOT_APPID", "appid")
	t.Setenv("QQ_BOT_APPSECRET", "secret")
	t.Setenv("BOT_ENABLE_QQ_BOT", "false")

	cfg := FromEnv()
	if cfg.EnableQQBot {
		t.Fatal("EnableQQBot = true, want false")
	}
}

func TestFromEnvNormalizesNapCatReversePath(t *testing.T) {
	t.Setenv("NAPCAT_REVERSE_PATH", " ws ")

	cfg := FromEnv()
	if cfg.NapCatReversePath != "/ws" {
		t.Fatalf("NapCatReversePath = %q", cfg.NapCatReversePath)
	}
}

func TestFromEnvKeepsAbsoluteNapCatReversePath(t *testing.T) {
	t.Setenv("NAPCAT_REVERSE_PATH", " /custom ")

	cfg := FromEnv()
	if cfg.NapCatReversePath != "/custom" {
		t.Fatalf("NapCatReversePath = %q", cfg.NapCatReversePath)
	}
}
