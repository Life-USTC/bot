package auth

import (
	"context"
	"log"
	"strings"
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
		if !hasLoginPollIdentity(ident) {
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
		if message == "" {
			continue
		}
		if err := p.Notifier.SendLoginMessage(ctx, ident, message); err != nil {
			p.logf("send login notification failed: %v", err)
		}
	}
}

func (p *LoginPoller) notifyApproved(ctx context.Context, ident store.Identity, deviceCode string) {
	if !hasNotificationIdentity(ident) {
		return
	}
	if err := p.Notifier.SendLoginMessage(ctx, ident, "登录完成。"); err != nil {
		p.logf("send login notification failed: %v", err)
		p.markLoginSession(ctx, ident, deviceCode, "notify_failed")
		return
	}
	p.markLoginSession(ctx, ident, deviceCode, "approved")
}

func (p *LoginPoller) markLoginSession(ctx context.Context, ident store.Identity, deviceCode, status string) {
	if deviceCode == "" {
		return
	}
	if err := p.Manager.Store.MarkLoginSession(ctx, ident, deviceCode, status); err != nil {
		p.logf("mark login session %s failed: %v", status, err)
	}
}

func hasNotificationIdentity(ident store.Identity) bool {
	return strings.TrimSpace(ident.ConversationType) != "" &&
		strings.TrimSpace(ident.ConversationID) != ""
}

func hasLoginPollIdentity(ident store.Identity) bool {
	return strings.TrimSpace(ident.Platform) != "" &&
		strings.TrimSpace(ident.UserID) != "" &&
		hasNotificationIdentity(ident)
}

func (p *LoginPoller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}
