package auth

import (
	"context"
	"log"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type LoginPoller struct {
	Manager  *Manager
	Interval time.Duration
	Logger   *log.Logger
}

func (p *LoginPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 10 * time.Second
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

func (p *LoginPoller) tick(ctx context.Context) {
	if p.Manager == nil || p.Manager.Store == nil {
		return
	}
	sessions, err := p.Manager.Store.PendingLoginSessions(ctx)
	if err != nil {
		p.logf("list pending login sessions failed: %v", err)
		return
	}
	for _, session := range sessions {
		ident := session.Identity
		if !store.HasConversationIdentity(ident) {
			continue
		}
		result, err := p.Manager.PollDeviceLoginAndNotify(ctx, ident)
		if err != nil {
			p.logf("poll login session failed: %v", err)
			continue
		}
		if result.Pending || result.SlowDown {
			continue
		}
	}
}

func (p *LoginPoller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}
