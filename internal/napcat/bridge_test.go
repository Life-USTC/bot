package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/gorilla/websocket"

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

func TestSendTrimsAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer token" {
			t.Fatalf("authorization = %q", auth)
		}
		if got := r.URL.Query().Get("access_token"); got != "token" {
			t.Fatalf("access_token = %q", got)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, AccessToken: " token ", HTTPClient: server.Client()}
	if err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestSendTrimsAPIURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: " " + server.URL + "/// ", HTTPClient: server.Client()}
	if err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send_private_msg" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestSendReturnsNapCatJSONFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"failed","retcode":1200,"message":"send failed"}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendReturnsInvalidNapCatJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendReturnsNapCatHTTPFailureBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "502: upstream unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendReturnsNapCatRetcodeFailureFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retcode":1}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "retcode 1: unknown") {
		t.Fatalf("error = %v", err)
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

func TestRunTrimsAccessToken(t *testing.T) {
	upgrader := websocket.Upgrader{}
	authHeader := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader <- r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	bridge := &Bridge{WSURL: "ws" + server.URL[len("http"):], AccessToken: " token "}
	if err := bridge.Run(context.Background()); err == nil {
		t.Fatal("Run returned nil after websocket close")
	}
	if auth := <-authHeader; auth != "Bearer token" {
		t.Fatalf("authorization = %q", auth)
	}
}

func TestHandleMessageRecordsIgnored(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	bridge := &Bridge{Handler: commands.Handler{Store: db, Prefix: "/life"}}
	reply, ok := bridge.handleMessage(context.Background(), messageEvent{
		PostType:    "message",
		MessageType: "private",
		RawMessage:  "not a command",
		UserID:      456,
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := db.InteractionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestSendLoginMessageUsesIdentity(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	if err := bridge.SendLoginMessage(context.Background(), store.Identity{
		UserID:           "42",
		ConversationType: "private",
		ConversationID:   "42",
	}, "登录完成。"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send_private_msg" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["user_id"].(float64) != 42 || gotBody["message"] != "登录完成。" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestSendMessageNormalizesGroupIdentity(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	if err := bridge.SendMessage(context.Background(), store.Identity{
		UserID:           " 42 ",
		ConversationType: " GROUP ",
		ConversationID:   " 100 ",
	}, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send_group_msg" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["group_id"].(float64) != 100 || gotBody["message"] != "hello" {
		t.Fatalf("body = %#v", gotBody)
	}
	if _, ok := gotBody["user_id"]; ok {
		t.Fatalf("group payload should not include user_id: %#v", gotBody)
	}
}

func TestSendMessageRejectsInvalidIdentityIDs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	err := bridge.SendMessage(context.Background(), store.Identity{
		UserID:           "not-a-number",
		ConversationType: "private",
		ConversationID:   "not-a-number",
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "invalid napcat user id") {
		t.Fatalf("private error = %v", err)
	}
	err = bridge.SendMessage(context.Background(), store.Identity{
		UserID:           "42",
		ConversationType: "group",
		ConversationID:   "not-a-number",
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "invalid napcat group id") {
		t.Fatalf("group error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("unexpected HTTP requests = %d", requests)
	}
}

func TestSendLoginMessageUsesActiveReverseWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	ready := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		bridge := &Bridge{}
		connID := bridge.setReverseConn(conn, &sync.Mutex{})
		defer bridge.clearReverseConn(connID)
		if err := bridge.SendLoginMessage(context.Background(), store.Identity{
			UserID:           "42",
			ConversationType: "private",
			ConversationID:   "42",
		}, "登录完成。"); err != nil {
			t.Error(err)
			return
		}
		ready <- struct{}{}
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	<-ready
	if frame["action"] != "send_private_msg" {
		t.Fatalf("action = %v", frame["action"])
	}
	params := frame["params"].(map[string]any)
	if params["user_id"].(float64) != 42 || params["message"] != "登录完成。" {
		t.Fatalf("params = %#v", params)
	}
}

func TestTrimLogText(t *testing.T) {
	if got := trimLogText("short"); got != "short" {
		t.Fatalf("short text = %q", got)
	}
	longASCII := strings.Repeat("a", 161)
	if got := trimLogText(longASCII); got != strings.Repeat("a", 160)+"..." {
		t.Fatalf("ASCII trim = %q", got)
	}
	longChinese := strings.Repeat("校", 161)
	got := trimLogText(longChinese)
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed text is invalid UTF-8: %q", got)
	}
	if got != strings.Repeat("校", 160)+"..." {
		t.Fatalf("Chinese trim = %q", got)
	}
}
