package textutil

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeLogTextRemovesURLsAndCredentials(t *testing.T) {
	input := `POST https://life.example/private?token=query returned 500: {"access_token":"secret-value"} Authorization: Bearer eyJabcdefgh.ijklmnop.qrstuvwx`
	got := SafeLogText(input)
	for _, secret := range []string{"life.example", "query", "secret-value", "eyJabcdefgh", "ijklmnop", "qrstuvwx"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SafeLogText leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "<url>") || !strings.Contains(got, "<redacted>") {
		t.Fatalf("SafeLogText = %q", got)
	}
}

func TestSafeLogErrorBoundsDiagnosticSize(t *testing.T) {
	got := SafeLogError(errors.New(strings.Repeat("x", 1200)))
	if len([]rune(got)) != 1001 || !strings.HasSuffix(got, "…") {
		t.Fatalf("SafeLogError length = %d, suffix = %q", len([]rune(got)), got[len(got)-3:])
	}
}
