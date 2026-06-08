package onebot12

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libob "github.com/botuniverse/go-libonebot"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestIdentityFromParamsDefaultsOptionalFields(t *testing.T) {
	ident, ok := identityFromParams(libob.ResponseWriter{}, &libob.Request{
		Params: libob.EasierMapFromMap(map[string]interface{}{
			"user_id": "42",
		}),
	})
	if !ok {
		t.Fatal("identity was not parsed")
	}
	want := store.Identity{
		Platform:         "onebot",
		UserID:           "42",
		ConversationType: "private",
		ConversationID:   "42",
	}
	if ident != want {
		t.Fatalf("identity = %#v, want %#v", ident, want)
	}
}

func TestStatusWithoutLifeClientReportsOffline(t *testing.T) {
	server := New(Config{SelfID: "bot"}, nil)
	resp := server.onebot.CallAction(libob.ActionGetStatus, nil)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, message = %q", resp.Status, resp.Message)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", resp.Data)
	}
	if data["good"] != false || data["online"] != false {
		t.Fatalf("data = %#v", data)
	}
}

func TestLifeActionWithoutLifeClientFails(t *testing.T) {
	server := New(Config{SelfID: "bot"}, nil)
	resp := server.onebot.CallAction(actionPrefix+".get_current_semester", nil)
	if resp.Status != "failed" || resp.RetCode != libob.RetCodeUnsupportedAction {
		t.Fatalf("response = %#v", resp)
	}
	if !strings.Contains(resp.Message, "Life @ USTC API is not configured") {
		t.Fatalf("message = %q", resp.Message)
	}
}

func TestTodosRefreshesUnauthorizedToken(t *testing.T) {
	ctx := context.Background()
	ident := store.Identity{Platform: "onebot", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var serverURL string
	refreshRequests := 0
	todoRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	mux.HandleFunc("/api/todos", func(w http.ResponseWriter, r *http.Request) {
		todoRequests++
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "Bearer new-access":
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告"}]}`))
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	})
	lifeServer := httptest.NewServer(mux)
	defer lifeServer.Close()
	serverURL = lifeServer.URL

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Resource:     lifeServer.URL,
	}); err != nil {
		t.Fatal(err)
	}

	server := New(Config{
		SelfID: "bot",
		Auth:   &auth.Manager{Server: lifeServer.URL, HTTPClient: lifeServer.Client(), Store: db},
	}, life.NewClient(lifeServer.URL, lifeServer.Client()))
	resp := server.onebot.CallAction(actionPrefix+".list_todos", map[string]interface{}{
		"user_id": "42",
	})
	if resp.Status != "ok" {
		t.Fatalf("response = %#v", resp)
	}
	if refreshRequests != 1 || todoRequests != 2 {
		t.Fatalf("refreshRequests = %d, todoRequests = %d", refreshRequests, todoRequests)
	}
	cred, err := db.Credential(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.AccessToken != "new-access" || cred.RefreshToken != "new-refresh" {
		t.Fatalf("credential = %#v", cred)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestErrorRetCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: libob.RetCodeNetworkError},
		{name: "handler timeout", err: http.ErrHandlerTimeout, want: libob.RetCodeNetworkError},
		{name: "net timeout", err: timeoutError{}, want: libob.RetCodeNetworkError},
		{name: "regular", err: errors.New("boom"), want: libob.RetCodeInternalHandlerError},
	}
	for _, tt := range tests {
		if got := errorRetCode(tt.err); got != tt.want {
			t.Fatalf("%s: errorRetCode = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestIsLifeAPIError(t *testing.T) {
	if !isLifeAPIError(life.HTTPError{StatusCode: http.StatusBadGateway}) {
		t.Fatal("life HTTP error was not classified as API error")
	}
	if isLifeAPIError(errors.New("sqlite failure")) {
		t.Fatal("generic error was classified as API error")
	}
}

func TestContextWithTimeoutSetsDeadline(t *testing.T) {
	ctx, cancel := ContextWithTimeout()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("deadline was not set")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 15*time.Second {
		t.Fatalf("deadline remaining = %s", remaining)
	}
}

func TestIdentityFromParamsTrimsFields(t *testing.T) {
	ident, ok := identityFromParams(libob.ResponseWriter{}, &libob.Request{
		Params: libob.EasierMapFromMap(map[string]interface{}{
			"platform":          " onebot ",
			"user_id":           " 42 ",
			"conversation_type": " group ",
			"conversation_id":   " 100 ",
		}),
	})
	if !ok {
		t.Fatal("identity was not parsed")
	}
	want := store.Identity{
		Platform:         "onebot",
		UserID:           "42",
		ConversationType: "group",
		ConversationID:   "100",
	}
	if ident != want {
		t.Fatalf("identity = %#v, want %#v", ident, want)
	}
}
