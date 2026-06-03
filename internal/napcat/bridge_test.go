package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestSendGroupMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if auth := r.Header.Get("Authorization"); auth != "Bearer token" {
			t.Fatalf("authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, AccessToken: "token", HTTPClient: server.Client()}
	err := bridge.Send(context.Background(), messageEvent{
		PostType:    "message",
		MessageType: "group",
		GroupID:     123,
		UserID:      456,
	}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send_group_msg" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["group_id"].(float64) != 123 || gotBody["message"] != "hello" {
		t.Fatalf("body = %#v", gotBody)
	}
	if _, ok := gotBody["user_id"]; ok {
		t.Fatalf("group payload should not include user_id: %#v", gotBody)
	}
}

func TestSendReverseReply(t *testing.T) {
	upgrader := websocket.Upgrader{}
	done := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Error(err)
			return
		}
		done <- frame
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	err = sendReverseReply(conn, nil, messageEvent{MessageType: "private", UserID: 42}, "pong")
	if err != nil {
		t.Fatal(err)
	}
	frame := <-done
	if frame["action"] != "send_private_msg" {
		t.Fatalf("action = %v", frame["action"])
	}
	params := frame["params"].(map[string]any)
	if params["user_id"].(float64) != 42 || params["message"] != "pong" {
		t.Fatalf("params = %#v", params)
	}
}

func TestReverseBridgeEndToEnd(t *testing.T) {
	lifeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/courses" {
			t.Fatalf("unexpected Life API path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer lifeServer.Close()

	bridge := &Bridge{
		Handler: commands.Handler{
			Life:   life.NewClient(lifeServer.URL, lifeServer.Client()),
			Prefix: "/life",
		},
	}
	upgrader := websocket.Upgrader{}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		go bridge.handleReverseConn(context.Background(), conn)
	}))
	defer wsServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+wsServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	err = conn.WriteJSON(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"raw_message":  "/life course calculus",
		"user_id":      456,
	})
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame["action"] != "send_private_msg" {
		t.Fatalf("action = %v", frame["action"])
	}
	params := frame["params"].(map[string]any)
	if !strings.Contains(params["message"].(string), "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷      \tCalculus") {
		t.Fatalf("message = %q", params["message"])
	}
}

func TestLoginPollSendsCompletionMessage(t *testing.T) {
	var serverURL string
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_endpoint": serverURL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "openid profile email offline_access",
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

	bridge := &Bridge{
		Handler:           commands.Handler{Auth: &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: s}},
		LoginPollInterval: 10 * time.Millisecond,
		LoginPollTimeout:  200 * time.Millisecond,
	}
	sent := make(chan string, 1)
	bridge.startLoginPoll(context.Background(), messageEvent{MessageType: "private", UserID: 42}, func(ctx context.Context, event messageEvent, message string) error {
		sent <- message
		return nil
	})

	select {
	case got := <-sent:
		if got != "登录成功。" {
			t.Fatalf("message = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for login completion message")
	}
}
