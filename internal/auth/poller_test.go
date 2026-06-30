package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type fakeNotifier struct {
	ident     store.Identity
	message   string
	failCount int
	afterSend func()
}

func (n *fakeNotifier) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	if n.failCount > 0 {
		n.failCount--
		return fmt.Errorf("send failed")
	}
	n.ident = ident
	n.message = message
	if n.afterSend != nil {
		n.afterSend()
	}
	return nil
}

func TestLoginPollerRetriesFailedCompletionNotification(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLoginSession(context.Background(), ident, "device", "notify_failed"); err != nil {
		t.Fatal(err)
	}

	notifier := &fakeNotifier{failCount: 1}
	poller := LoginPoller{
		Manager:  &Manager{Store: s},
		Notifier: notifier,
	}
	poller.tick(context.Background())
	if notifier.message != "" {
		t.Fatalf("message = %q", notifier.message)
	}
	sessions, err := s.PendingLoginSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != "notify_failed" {
		t.Fatalf("sessions = %#v", sessions)
	}

	poller.tick(context.Background())
	if notifier.message != "登录完成。" {
		t.Fatalf("message = %q", notifier.message)
	}
	sessions, err = s.PendingLoginSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestLoginPollerSkipsBlankNotificationIdentity(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{
		Platform:         "napcat",
		UserID:           "42",
		ConversationType: "  ",
		ConversationID:   "  ",
	}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLoginSession(context.Background(), ident, "device", "notify_failed"); err != nil {
		t.Fatal(err)
	}

	notifier := &fakeNotifier{}
	poller := LoginPoller{
		Manager:  &Manager{Store: s},
		Notifier: notifier,
	}
	poller.tick(context.Background())
	if notifier.message != "" {
		t.Fatalf("message = %q", notifier.message)
	}
	sessions, err := s.PendingLoginSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != "notify_failed" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestLoginPollerLogsMarkLoginSessionFailure(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	var logs bytes.Buffer
	notifier := &fakeNotifier{}
	poller := LoginPoller{
		Manager:  &Manager{Store: s},
		Notifier: notifier,
		Logger:   log.New(&logs, "", 0),
	}
	ident := store.Identity{ConversationType: "private", ConversationID: "42"}
	poller.notifyApproved(context.Background(), ident, "device")

	if notifier.message != "登录完成。" {
		t.Fatalf("message = %q", notifier.message)
	}
	if !strings.Contains(logs.String(), "mark login session approved failed") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestLoginPollerSendsCompletionFromPendingSession(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         oauthScope,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode:      "device",
		ClientID:        "client",
		ExpiresAt:       time.Now().Add(time.Minute),
		IntervalSeconds: 10,
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}

	notifier := &fakeNotifier{}
	poller := LoginPoller{
		Manager:  &Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Notifier: notifier,
	}
	poller.tick(context.Background())

	if notifier.message != "登录完成。" {
		t.Fatalf("message = %q", notifier.message)
	}
	if notifier.ident != ident {
		t.Fatalf("identity = %#v", notifier.ident)
	}
	active, err := s.ActiveLoginSession(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if active != nil {
		t.Fatalf("session still pending: %#v", active)
	}
}

func TestLoginPollerRunsImmediateTick(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLoginSession(context.Background(), ident, "device", "notify_failed"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	notifier := &fakeNotifier{afterSend: cancel}
	poller := LoginPoller{
		Manager:  &Manager{Store: s},
		Notifier: notifier,
		Interval: time.Hour,
	}
	poller.Run(ctx)

	if notifier.message != "登录完成。" {
		t.Fatalf("message = %q", notifier.message)
	}
}
