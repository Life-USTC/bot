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
