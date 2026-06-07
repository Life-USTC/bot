package config

import "testing"

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
