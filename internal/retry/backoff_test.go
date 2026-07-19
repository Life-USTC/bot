package retry

import (
	"context"
	"testing"
	"time"
)

func TestBackoffGrowsAndCaps(t *testing.T) {
	backoff := Backoff{Initial: 3 * time.Second, Max: 10 * time.Second}
	want := []time.Duration{3 * time.Second, 6 * time.Second, 10 * time.Second, 10 * time.Second}
	for failures, expected := range want {
		if got := backoff.Duration(failures); got != expected {
			t.Fatalf("Duration(%d) = %s, want %s", failures, got, expected)
		}
	}
}

func TestBackoffJitterStaysWithinBounds(t *testing.T) {
	backoff := Backoff{Initial: 10 * time.Second, Max: time.Minute, Jitter: 0.2}
	for range 100 {
		got := backoff.Duration(0)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered duration = %s", got)
		}
	}
}

func TestBackoffJitterDoesNotExceedCap(t *testing.T) {
	backoff := Backoff{Initial: 10 * time.Second, Max: 10 * time.Second, Jitter: 0.2}
	for range 100 {
		got := backoff.Duration(0)
		if got < 8*time.Second || got > 10*time.Second {
			t.Fatalf("capped jittered duration = %s", got)
		}
	}
}

func TestWaitStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Wait(ctx, time.Hour) {
		t.Fatal("Wait returned true after cancellation")
	}
}
