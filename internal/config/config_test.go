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
			if cfg.HTTPClientTimeout != 15*time.Second {
				t.Fatalf("HTTPClientTimeout = %s, want 15s", cfg.HTTPClientTimeout)
			}
		})
	}
}

func TestFromEnvTrimsOptionalStrings(t *testing.T) {
	t.Setenv("BOT_ONEBOT_ACCESS_TOKEN", " onebot-token ")
	t.Setenv("NAPCAT_ACCESS_TOKEN", " napcat-token ")
	t.Setenv("NAPCAT_WS_URL", " ws://127.0.0.1:3001 ")
	t.Setenv("OPENAI_API_KEY", " api-key ")
	t.Setenv("OPENAI_BASE_URL", " https://llm.example/v1 ")

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
	if cfg.LLMAPIKey != "api-key" {
		t.Fatalf("LLMAPIKey = %q", cfg.LLMAPIKey)
	}
	if cfg.LLMBaseURL != "https://llm.example/v1" {
		t.Fatalf("LLMBaseURL = %q", cfg.LLMBaseURL)
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
