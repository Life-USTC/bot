package textutil

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "fallback", "later"); got != "fallback" {
		t.Fatalf("FirstNonEmpty = %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty empty = %q", got)
	}
}
