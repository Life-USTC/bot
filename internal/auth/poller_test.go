package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type fakeNotifier struct {
	ident   store.Identity
	message string
}

func (n *fakeNotifier) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	n.ident = ident
	n.message = message
	return nil
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

	if notifier.message != "登录成功。" {
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
