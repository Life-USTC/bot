package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

type Backoff struct {
	Initial time.Duration
	Max     time.Duration
	Jitter  float64
}

func (b Backoff) Duration(failures int) time.Duration {
	delay := b.Initial
	if delay <= 0 {
		delay = time.Second
	}
	maxDelay := b.Max
	if maxDelay < delay {
		maxDelay = delay
	}
	for i := 0; i < failures && delay < maxDelay; i++ {
		if delay > maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if b.Jitter <= 0 {
		return delay
	}
	jitter := min(b.Jitter, 1)
	factor := 1 - jitter + rand.Float64()*2*jitter
	return min(time.Duration(float64(delay)*factor), maxDelay)
}

func Wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
