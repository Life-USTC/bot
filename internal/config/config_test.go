package config

import (
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
	for _, value := range []string{"bad", "-1", "70000"} {
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
