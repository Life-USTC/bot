package notify

import (
	"context"
	"log"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

// RecurringPoller schedules local rules independently of OAuth and campus
// fetches. The ordinary delivery worker owns rendering, sends, and retries.
type RecurringPoller struct {
	Store    *store.Store
	Interval time.Duration
	Now      func() time.Time
	Logger   *log.Logger
}

func (p *RecurringPoller) Run(ctx context.Context) {
	if p.Store == nil {
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *RecurringPoller) tick(ctx context.Context) {
	if p.Store == nil {
		return
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if err := p.Store.EnqueueDueRecurringReminders(ctx, now); err != nil && p.Logger != nil {
		p.Logger.Printf("schedule recurring reminders failed: %v", err)
	}
}
