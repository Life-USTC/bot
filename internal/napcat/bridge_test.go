package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

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
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":101}}`))
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

func TestSendPayloadReturnsPlatformAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":"message-123"}}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	receipt, err := bridge.sendPayload(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PlatformMessageID != "message-123" || receipt.AcceptedAt.IsZero() {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestSendResponsePostsImageSegmentWhenAvailable(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":102}}`))
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bridge := Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	response := commands.Response{
		Text:  "今天课表：\n数据库系统",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表：\n数据库系统"),
	}

	err := bridge.SendResponse(context.Background(), messageEvent{MessageType: "private", UserID: 456}, response)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := gotBody["message"].([]any)
	if !ok || len(message) != 1 {
		t.Fatalf("message = %#v", gotBody["message"])
	}
	segment := message[0].(map[string]any)
	if segment["type"] != "image" {
		t.Fatalf("segment = %#v", segment)
	}
	data := segment["data"].(map[string]any)
	if !strings.HasPrefix(data["file"].(string), server.URL+"/media/") {
		t.Fatalf("file = %q", data["file"])
	}
}

func TestSendRichMessagePostsImageSegmentForIdentity(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":103}}`))
	}))
	defer server.Close()

	bridge := Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	image := responses.NewTextImage("class_reminder", "课前提醒", "课前提醒：\n数据库系统")
	err := bridge.SendRichMessage(context.Background(), store.Identity{
		Platform:         "napcat",
		UserID:           "456",
		ConversationType: "private",
		ConversationID:   "456",
	}, image.AltText, image)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := gotBody["message"].([]any)
	if !ok || len(message) != 1 || message[0].(map[string]any)["type"] != "image" {
		t.Fatalf("message = %#v", gotBody["message"])
	}
}

func TestSendResponseFallsBackToTextWhenImagePostFails(t *testing.T) {
	requests := 0
	var fallbackBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"status":"failed","message":"image rejected"}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&fallbackBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":104}}`))
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bridge := Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	response := commands.Response{
		Text:  "待办：\n1. 写报告",
		Image: responses.NewTextImage("todo", "待办", "待办：\n1. 写报告"),
	}

	err := bridge.SendResponse(context.Background(), messageEvent{MessageType: "private", UserID: 456}, response)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if fallbackBody["message"] != response.Text {
		t.Fatalf("fallback body = %#v", fallbackBody)
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
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":105}}`))
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

func testResponseFontPath(t *testing.T) string {
	t.Helper()
	for _, path := range responses.DefaultFontPathsForTest() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no CJK font found")
	return ""
}

func TestSendTrimsAPIURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":106}}`))
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

func TestSendReturnsNapCatHTTPFailureReadError(t *testing.T) {
	bridge := Bridge{
		APIURL: "https://napcat.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(errReader{}),
			}, nil
		})},
	}
	err := bridge.Send(context.Background(), messageEvent{
		MessageType: "private",
		UserID:      456,
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "502: read response body: read failed") {
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

	err = writeReverseAction(conn, nil, map[string]any{
		"action": "send_private_msg",
		"params": map[string]any{"user_id": int64(42), "message": "pong"},
		"echo":   "test-echo",
	})
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

func TestSendReverseReplyWaitsForMatchingAcceptance(t *testing.T) {
	upgrader := websocket.Upgrader{}
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
		_ = conn.WriteJSON(map[string]any{
			"status":  "ok",
			"retcode": 0,
			"data":    map[string]any{"message_id": 987654321},
			"echo":    frame["echo"],
		})
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	bridge := &Bridge{reverseTimeout: time.Second}
	readDone := make(chan error, 1)
	go func() {
		var raw json.RawMessage
		err := conn.ReadJSON(&raw)
		if err == nil && !bridge.resolveReverseAction(raw) {
			err = errors.New("action response was not resolved")
		}
		readDone <- err
	}()

	receipt, err := bridge.sendReverseReply(context.Background(), conn, nil, messageEvent{
		MessageType: "private",
		UserID:      42,
	}, "pong")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if receipt.PlatformMessageID != "987654321" || receipt.AcceptedAt.IsZero() {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestSendReverseReplyTimeoutIsUncertain(t *testing.T) {
	upgrader := websocket.Upgrader{}
	frameRead := make(chan struct{})
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
		close(frameRead)
		<-r.Context().Done()
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{reverseTimeout: 20 * time.Millisecond}
	_, err = bridge.sendReverseReply(context.Background(), conn, nil, messageEvent{
		MessageType: "private",
		UserID:      42,
	}, "pong")
	_ = conn.Close()
	<-frameRead
	if err == nil || !isUncertainSendError(err) {
		t.Fatalf("timeout error = %v", err)
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
	if err := conn.WriteJSON(map[string]any{
		"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 9001}, "echo": frame["echo"],
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReverseBridgeRepliesOnMessageConnectionAfterNewerConnectionCloses(t *testing.T) {
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

	wsURL := "ws" + wsServer.URL[len("http"):]
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn1.Close() }()
	waitForReverseSeq(t, bridge, 1)

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForReverseSeq(t, bridge, 2)
	_ = conn2.Close()
	waitForNoActiveReverseConn(t, bridge)

	err = conn1.WriteJSON(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"raw_message":  "/life course calculus",
		"user_id":      456,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn1.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := conn1.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame["action"] != "send_private_msg" {
		t.Fatalf("action = %v", frame["action"])
	}
	params := frame["params"].(map[string]any)
	if !strings.Contains(params["message"].(string), "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷      \tCalculus") {
		t.Fatalf("message = %q", params["message"])
	}
	if err := conn1.WriteJSON(map[string]any{
		"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 9002}, "echo": frame["echo"],
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForReverseSeq(t *testing.T, bridge *Bridge, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bridge.reverseMu.Lock()
		got := bridge.reverseSeq
		bridge.reverseMu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	bridge.reverseMu.Lock()
	got := bridge.reverseSeq
	bridge.reverseMu.Unlock()
	t.Fatalf("reverseSeq = %d, want at least %d", got, want)
}

func waitForNoActiveReverseConn(t *testing.T, bridge *Bridge) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, _ := bridge.activeReverseConn()
		if conn == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("active reverse connection was not cleared")
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
	if ok || reply.Text != "" {
		t.Fatalf("reply = %#v, ok = %v", reply, ok)
	}
	count, err := db.InteractionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestRecordIgnoredLogsStoreError(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var logs bytes.Buffer
	bridge := &Bridge{
		Handler: commands.Handler{Store: db, Prefix: "/life"},
		Logger:  log.New(&logs, "", 0),
	}
	reply, ok := bridge.handleMessage(context.Background(), messageEvent{
		PostType:   "message",
		RawMessage: "not a command",
		UserID:     456,
	})
	if ok || reply.Text != "" {
		t.Fatalf("reply = %#v, ok = %v", reply, ok)
	}
	if !strings.Contains(logs.String(), "record ignored interaction failed") {
		t.Fatalf("logs = %q", logs.String())
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
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":107}}`))
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

func TestSendMessageUsesPrivateConversationIDWhenUserIDMissing(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":108}}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	if err := bridge.SendMessage(context.Background(), store.Identity{
		ConversationType: "private",
		ConversationID:   " 42 ",
	}, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotBody["user_id"].(float64) != 42 || gotBody["message"] != "hello" {
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
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":109}}`))
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
		go func() {
			var raw json.RawMessage
			if err := conn.ReadJSON(&raw); err != nil {
				t.Error(err)
				return
			}
			bridge.resolveReverseAction(raw)
		}()
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
	if err := conn.WriteJSON(map[string]any{
		"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 9010}, "echo": frame["echo"],
	}); err != nil {
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
