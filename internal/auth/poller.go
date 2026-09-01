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
	p.repairAuthorizedJobs(ctx)
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
		if result.Authorized {
			current, scopeErr := p.Manager.HasCurrentScopes(ctx, ident)
			if scopeErr != nil {
				p.logf("verify OAuth scopes after login failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
					ident.Platform, ident.ConversationType, ident.ConversationID, scopeErr)
				continue
			}
			if !current {
				continue
			}
			if err := p.Manager.Store.UnblockConversationJobsAfterAuth(ctx, session.Identity); err != nil {
				p.logf("unblock conversation jobs after login failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
					session.Identity.Platform, session.Identity.ConversationType, session.Identity.ConversationID, err)
			}
		}
	}
}

func (p *LoginPoller) repairAuthorizedJobs(ctx context.Context) {
	jobs, err := p.Manager.Store.WaitingAuthConversationJobs(ctx, time.Now().UTC())
	if err != nil {
		p.logf("list authorization-waiting conversation jobs failed: %v", err)
		return
	}
	checked := make(map[store.Identity]bool, len(jobs))
	for _, job := range jobs {
		ident := job.Identity
		if checked[ident] {
			continue
		}
		checked[ident] = true
		current, err := p.Manager.HasCurrentScopes(ctx, ident)
		if err != nil {
			continue
		}
		if !current {
			continue
		}
		if err := p.Manager.Store.UnblockConversationJobsAfterAuth(ctx, ident); err != nil {
			p.logf("repair authorized conversation jobs failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
				ident.Platform, ident.ConversationType, ident.ConversationID, err)
		}
	}
}

func (p *LoginPoller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}
