package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/store"
)

func approvedLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": serverURL + "/token"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "refresh_token": "refresh",
			"token_type": "Bearer", "expires_in": 3600, "scope": oauthScope,
		})
	})
	server := httptest.NewServer(mux)
	serverURL = server.URL
	return server
}

func TestLoginPollerPublishesCompletionToDurableOutbox(t *testing.T) {
	server := approvedLoginServer(t)
	defer server.Close()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", ClientID: "client", ExpiresAt: time.Now().Add(time.Minute),
		IntervalSeconds: 10, Status: string(store.LoginStatusPending),
	}); err != nil {
		t.Fatal(err)
	}
	poller := LoginPoller{Manager: &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}}
	poller.tick(context.Background())

	records, err := s.ClaimDue(context.Background(), time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox records = %#v", records)
	}
	if got := records[0].Message.Target; got.Platform != "napcat" || got.Type != "private" || got.ID != "42" {
		t.Fatalf("target = %#v", got)
	}
	if records[0].Message.Content.TextContent() != "登录完成。" || records[0].Status != delivery.StatusDelivering {
		t.Fatalf("record = %#v", records[0])
	}
	if active, err := s.ActiveLoginSession(context.Background(), ident); err != nil || active != nil {
		t.Fatalf("active session = %#v, err = %v", active, err)
	}
	if cred, err := s.Credential(context.Background(), ident); err != nil || cred == nil || cred.AccessToken != "access" {
		t.Fatalf("credential = %#v, err = %v", cred, err)
	}
}

func TestManualLoginStatusDoesNotPublishDuplicate(t *testing.T) {
	server := approvedLoginServer(t)
	defer server.Close()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", ClientID: "client", ExpiresAt: time.Now().Add(time.Minute),
		Status: string(store.LoginStatusPending),
	}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}
	result, err := manager.PollDeviceLogin(context.Background(), ident)
	if err != nil || result.Message != "登录完成。" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	records, err := s.ClaimDue(context.Background(), time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("manual status queued notifications: %#v", records)
	}
	poller := LoginPoller{Manager: &manager}
	poller.tick(context.Background())
	records, err = s.ClaimDue(context.Background(), time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("poller duplicated manual result: %#v", records)
	}
}

func TestConcurrentTerminalResultDoesNotRepeatCompletionMessage(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	session := store.LoginSession{DeviceCode: "device", ClientID: "client", ExpiresAt: time.Now().Add(time.Minute), Status: "pending"}
	if err := s.SaveLoginSession(context.Background(), ident, session); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Store: s}
	first, err := manager.finishLogin(context.Background(), ident, session, store.LoginStatusExpired, nil, "验证码已过期。发送：登录", true)
	if err != nil || first.Message != "验证码已过期。发送：登录" {
		t.Fatalf("first = %#v, err = %v", first, err)
	}
	second, err := manager.finishLogin(context.Background(), ident, session, store.LoginStatusExpired, nil, "验证码已过期。发送：登录", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Message == first.Message {
		t.Fatalf("terminal message repeated: %#v", second)
	}
}

func TestLoginPollerRunsImmediateTick(t *testing.T) {
	server := approvedLoginServer(t)
	defer server.Close()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", ClientID: "client", ExpiresAt: time.Now().Add(time.Minute),
		Status: string(store.LoginStatusPending),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	poller := LoginPoller{
		Manager:  &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Interval: time.Hour,
	}
	poller.Run(ctx)
	records, err := s.ClaimDue(context.Background(), time.Now().Add(time.Second), 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("immediate records = %#v, err = %v", records, err)
	}
}

func TestLoginPollerUnblocksConversationJobWithLoginIdentity(t *testing.T) {
	server := approvedLoginServer(t)
	defer server.Close()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, _, err := s.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "event-auth", State: store.ConversationJobStateWaitingAuth,
		Input: store.ConversationJobInput{Text: "查询明天课表"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode: "device", ClientID: "client", ExpiresAt: time.Now().Add(time.Minute),
		IntervalSeconds: 1, Status: string(store.LoginStatusPending),
	}); err != nil {
		t.Fatal(err)
	}
	poller := LoginPoller{
		Manager: &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
	}
	poller.tick(ctx)
	saved, err := s.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateQueued || saved.Identity != ident {
		t.Fatalf("unblocked job = %#v", saved)
	}
}

func TestLoginPollerRepairsAuthorizedJobAfterRestart(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ident := store.Identity{Platform: "qqbot", UserID: "resume", ConversationType: "private", ConversationID: "resume"}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Scope: oauthScope,
	}); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "event-restart", State: store.ConversationJobStateWaitingAuth,
		Input: store.ConversationJobInput{Text: "查询作业"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	poller := LoginPoller{Manager: &Manager{Store: s}}
	poller.tick(ctx)
	saved, err := s.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateQueued {
		t.Fatalf("job after restart repair = %#v", saved)
	}
	_ = s.Close()
}

func TestLoginPollerDoesNotReleaseJobForIncompleteGrant(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := store.Identity{Platform: "qqbot", UserID: "stale-scope", ConversationType: "private", ConversationID: "stale-scope"}
	if err := s.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour),
		Scope: strings.ReplaceAll(oauthScope, "workspace.calendar-feed:read", ""),
	}); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "event-stale-scope", State: store.ConversationJobStateWaitingAuth,
		Input: store.ConversationJobInput{Text: "查询作业"},
	})
	if err != nil {
		t.Fatal(err)
	}

	(&LoginPoller{Manager: &Manager{Store: s}}).tick(ctx)
	saved, err := s.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingAuth {
		t.Fatalf("incomplete grant released job = %#v", saved)
	}
}
