package auth

import (
	"context"
	"log"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type LoginPoller struct {
	Manager         *Manager
	Interval        time.Duration
	Logger          *log.Logger
	Resume          ResumePendingCallback
	ResumeBatchSize int
}

// ResumePendingCallback replays one already-claimed text request. The
// callback must return only after the request has been handed to the bot
// application; the poller completes or releases the durable claim based on
// that result.
type ResumePendingCallback func(context.Context, store.PendingRequest) error

const defaultResumeBatchSize = 100

func (p *LoginPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	p.ResumePending(ctx)
	p.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Retry durable requests first. A request may have failed to resume
			// after a prior authorization, or may have survived a process restart.
			p.ResumePending(ctx)
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
		if result.Authorized {
			// Use the identity persisted with the login session. It is the
			// original private conversation, not an identity reconstructed from
			// the poller's current loop or a callback closure.
			p.resumeOne(ctx, session.Identity)
		}
	}
}

// ResumePending retries eligible requests for users that already have a
// credential. Run calls this on startup and on every polling cycle so a claim
// is not lost if the process exits after authorization but before completion.
func (p *LoginPoller) ResumePending(ctx context.Context) {
	if p == nil || p.Manager == nil || p.Manager.Store == nil || p.Resume == nil {
		return
	}
	limit := p.ResumeBatchSize
	if limit <= 0 {
		limit = defaultResumeBatchSize
	}
	requests, err := p.Manager.Store.PendingRequests(ctx, time.Now(), limit)
	if err != nil {
		p.logf("list pending requests failed: %v", err)
		return
	}
	for _, request := range requests {
		// Do not consume a request while a new device login is still pending.
		// An old credential can remain in the store during reauthorization.
		loginSession, err := p.Manager.Store.ActiveLoginSession(ctx, request.Identity)
		if err != nil {
			p.logf("check pending login before request resume failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
				request.Identity.Platform, request.Identity.ConversationType, request.Identity.ConversationID, err)
			continue
		}
		if loginSession != nil {
			continue
		}
		credential, err := p.Manager.Store.Credential(ctx, request.Identity)
		if err != nil {
			p.logf("check credential before request resume failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
				request.Identity.Platform, request.Identity.ConversationType, request.Identity.ConversationID, err)
			continue
		}
		if credential == nil {
			continue
		}
		p.resumeOne(ctx, request.Identity)
	}
}

func (p *LoginPoller) resumeOne(ctx context.Context, ident store.Identity) {
	if p == nil || p.Manager == nil || p.Manager.Store == nil || p.Resume == nil {
		return
	}
	request, err := p.Manager.Store.ClaimPendingRequest(ctx, ident)
	if err != nil {
		p.logf("claim pending request failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			ident.Platform, ident.ConversationType, ident.ConversationID, err)
		return
	}
	if request == nil {
		return
	}
	if err := p.Resume(ctx, *request); err != nil {
		if _, failErr := p.Manager.Store.FailPendingRequest(ctx, request.ID, request.ClaimToken, err.Error()); failErr != nil {
			p.logf("release failed pending request failed: id=%d error=%v", request.ID, failErr)
		}
		p.logf("resume pending request failed: id=%d error=%v", request.ID, err)
		return
	}
	completed, err := p.Manager.Store.CompletePendingRequest(ctx, request.ID, request.ClaimToken)
	if err != nil {
		p.logf("complete pending request failed: id=%d error=%v", request.ID, err)
		return
	}
	if !completed {
		p.logf("complete pending request lost claim: id=%d", request.ID)
	}
}

func (p *LoginPoller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}
