package auth

import (
	"context"
	"log"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type LoginNotifier interface {
	SendLoginMessage(ctx context.Context, ident store.Identity, message string) error
}

type LoginPoller struct {
	Manager  *Manager
	Notifier LoginNotifier
	Interval time.Duration
	Logger   *log.Logger
}

func (p *LoginPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
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
	if p.Manager == nil || p.Manager.Store == nil || p.Notifier == nil {
		return
	}
	sessions, err := p.Manager.Store.PendingLoginSessions(ctx)
	if err != nil {
		p.logf("list pending login sessions failed: %v", err)
		return
	}
	for _, session := range sessions {
		ident := session.Identity
		if ident.Platform == "" || ident.UserID == "" {
			continue
		}
		if session.Status == "notify_failed" {
			p.notifyApproved(ctx, ident, session.DeviceCode)
			continue
		}
		result, err := p.Manager.PollDeviceLogin(ctx, ident)
		if err != nil {
			p.logf("poll login session failed: %v", err)
			continue
		}
		if result.Pending || result.SlowDown {
			continue
		}
		message := result.Message
		if result.Authorized {
			p.notifyApproved(ctx, ident, session.DeviceCode)
			continue
		}
		if message == "" || ident.ConversationType == "" || ident.ConversationID == "" {
			continue
		}
		if err := p.Notifier.SendLoginMessage(ctx, ident, message); err != nil {
			p.logf("send login notification failed: %v", err)
		}
	}
}

func (p *LoginPoller) notifyApproved(ctx context.Context, ident store.Identity, deviceCode string) {
	if ident.ConversationType == "" || ident.ConversationID == "" {
		return
	}
	if err := p.Notifier.SendLoginMessage(ctx, ident, "登录成功。"); err != nil {
		p.logf("send login notification failed: %v", err)
		if deviceCode != "" {
			_ = p.Manager.Store.MarkLoginSession(ctx, ident, deviceCode, "notify_failed")
		}
		return
	}
	if deviceCode != "" {
		_ = p.Manager.Store.MarkLoginSession(ctx, ident, deviceCode, "approved")
	}
}

func (p *LoginPoller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}
