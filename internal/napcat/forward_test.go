package napcat

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/commands"
)

func TestAutoApproveFriendRequest(t *testing.T) {
	var gotBody map[string]any
	var mu sync.Mutex
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/set_friend_add_request" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
		close(done)
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	raw, _ := json.Marshal(map[string]any{
		"post_type":    "request",
		"request_type": "friend",
		"flag":         "flag-1",
		"user_id":      2047532941,
	})
	bridge.handleIncomingEvent(context.Background(), raw, func(context.Context, messageEvent) {
		t.Fatal("friend request should not dispatch as message")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for friend approve")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotBody["flag"] != "flag-1" || gotBody["approve"] != true {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestApprovePendingFriendRequests(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		switch r.URL.Path {
		case "/get_doubt_friends_add_request":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":[
				{"user_id":111,"nickname":"Alice","flag":"f1","reason":"hi"},
				{"user_id":222,"nickname":"Bob","flag":"f2","reason":""}
			]}`))
		case "/set_doubt_friends_add_request":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logs := &strings.Builder{}
	bridge := &Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Logger:     log.New(logs, "", 0),
	}
	// Bypass the startup delay by using a cancelled-after-wait pattern via direct call
	// with a context that is already past the timer: call the helper pieces instead.
	ctx := context.Background()
	response, err := bridge.callNapCatAction(ctx, "get_doubt_friends_add_request", map[string]any{"count": 100})
	if err != nil {
		t.Fatal(err)
	}
	items, err := parseDoubtFriendRequests(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	for _, item := range items {
		if err := bridge.approveDoubtFriendRequest(ctx, item.Flag); err != nil {
			t.Fatalf("approve %s: %v", item.Flag, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 3 {
		t.Fatalf("paths = %#v", paths)
	}
	if paths[0] != "/get_doubt_friends_add_request" {
		t.Fatalf("first path = %s", paths[0])
	}
	if bodies[1]["flag"] != "f1" || bodies[2]["flag"] != "f2" {
		t.Fatalf("bodies = %#v", bodies)
	}
}

func TestParseDoubtFriendRequestsWrapped(t *testing.T) {
	items, err := parseDoubtFriendRequests([]byte(`{"list":[{"user_id":1,"flag":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Flag != "x" {
		t.Fatalf("items = %#v", items)
	}
}

func TestParseDoubtFriendRequestsNapCatFields(t *testing.T) {
	items, err := parseDoubtFriendRequests([]byte(`[{"uin":2047532941,"nick":"Alice","flag":"u_abc","reason":"hi"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].displayUserID() != 2047532941 || items[0].displayNickname() != "Alice" || items[0].Flag != "u_abc" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestEnrichForwardMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_forward_msg" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[
			{"type":"node","data":{"user_id":"1","nickname":"Alice","content":[{"type":"text","data":{"text":"第一段"}}]}},
			{"type":"node","data":{"user_id":"2","nickname":"Bob","content":[{"type":"text","data":{"text":"第二段"}}]}}
		]}}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	event := messageEvent{
		PostType:    "message",
		MessageType: "private",
		UserID:      42,
		RawMessage:  "[CQ:forward,id=fwd-1]",
		Message: []map[string]any{{
			"type": "forward",
			"data": map[string]any{"id": "fwd-1"},
		}},
	}
	bridge.enrichMessageEvent(context.Background(), &event)
	if !strings.Contains(event.RawMessage, "Alice: 第一段") || !strings.Contains(event.RawMessage, "Bob: 第二段") {
		t.Fatalf("raw = %q", event.RawMessage)
	}
}

func TestEnrichForwardMessageNapCatMessageShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_forward_msg" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[
			{
				"sender":{"user_id":1001,"nickname":"小明"},
				"raw_message":"作业截图",
				"message":[{"type":"text","data":{"text":"作业截图"}},{"type":"image","data":{"url":"https://cdn.example/a.png"}}]
			},
			{
				"sender":{"user_id":1002,"nickname":"小红"},
				"raw_message":"好的",
				"message":[{"type":"text","data":{"text":"好的"}}]
			}
		]}}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	event := messageEvent{
		MessageType: "private",
		RawMessage:  "[CQ:forward,id=fwd-napcat]",
		Message: []any{map[string]any{
			"type": "forward",
			"data": map[string]any{"id": "fwd-napcat"},
		}},
	}
	bridge.enrichMessageEvent(context.Background(), &event)
	if !strings.Contains(event.RawMessage, "小明: 作业截图[图片]") || !strings.Contains(event.RawMessage, "小红: 好的") {
		t.Fatalf("raw = %q", event.RawMessage)
	}
	urls := event.imageURLs()
	if len(urls) != 1 || urls[0] != "https://cdn.example/a.png" {
		t.Fatalf("forward images = %#v", urls)
	}
}

func TestEnrichForwardMessageUsesInlineContent(t *testing.T) {
	bridge := &Bridge{}
	event := messageEvent{
		MessageType: "private",
		RawMessage:  "[合并转发]",
		Message: []any{map[string]any{
			"type": "forward",
			"data": map[string]any{
				"id": "fwd-inline",
				"content": []any{
					map[string]any{
						"type": "node",
						"data": map[string]any{
							"nickname": "A",
							"content": []any{
								map[string]any{"type": "text", "data": map[string]any{"text": "内联全文"}},
								map[string]any{"type": "image", "data": map[string]any{"url": "https://cdn.example/inline.png"}},
							},
						},
					},
				},
			},
		}},
	}
	bridge.enrichMessageEvent(context.Background(), &event)
	if !strings.Contains(event.RawMessage, "A: 内联全文[图片]") {
		t.Fatalf("raw = %q", event.RawMessage)
	}
	if strings.Contains(event.RawMessage, "无法读取") {
		t.Fatalf("should not fetch when inline content exists: %q", event.RawMessage)
	}
	urls := event.imageURLs()
	if len(urls) != 1 || urls[0] != "https://cdn.example/inline.png" {
		t.Fatalf("inline forward images = %#v", urls)
	}
}

func TestProjectBusStyleForwardIDsFromCQ(t *testing.T) {
	ids := forwardIDsFromCQMessage("[CQ:forward,id=abc123]")
	if len(ids) == 0 || ids[0] != "abc123" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestFormatForwardMessageTruncatesLargePayload(t *testing.T) {
	nodes := make([]any, 0, maxForwardNodes+5)
	for i := 0; i < maxForwardNodes+5; i++ {
		nodes = append(nodes, map[string]any{
			"data": map[string]any{
				"nickname": "U",
				"content":  []any{map[string]any{"type": "text", "data": map[string]any{"text": strings.Repeat("x", 20)}}},
			},
		})
	}
	raw, _ := json.Marshal(map[string]any{"messages": nodes})
	got := formatForwardMessageData(raw)
	if !strings.Contains(got, "合并转发已截断") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if strings.Count(got, "\n")+1 > maxForwardNodes+2 {
		t.Fatalf("too many lines after truncate: %q", got)
	}
}

func TestHandleIncomingEventDispatchesForwardWithoutNetwork(t *testing.T) {
	var dispatched messageEvent
	raw, _ := json.Marshal(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"user_id":      42,
		"raw_message":  "[CQ:forward,id=fwd-1]",
		"message": []map[string]any{{
			"type": "forward",
			"data": map[string]any{"id": "fwd-1"},
		}},
	})
	bridge := &Bridge{}
	bridge.handleIncomingEvent(context.Background(), raw, func(_ context.Context, event messageEvent) {
		dispatched = event
	})
	if dispatched.UserID != 42 {
		t.Fatalf("expected dispatch before enrich, got %#v", dispatched)
	}
}

type syncLogBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestReverseEnrichForwardUsesWebsocketAction(t *testing.T) {
	var logs syncLogBuffer
	bridge := &Bridge{
		Handler: commands.Handler{},
		Logger:  log.New(&logs, "", 0),
		// Intentionally no APIURL: production reverse-only must not need HTTP.
	}
	upgrader := websocket.Upgrader{}
	handled := make(chan struct{})
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			defer close(handled)
			bridge.handleReverseConn(context.Background(), conn)
		}()
	}))
	defer wsServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+wsServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"user_id":      2047532941,
		"raw_message":  "[CQ:forward,id=fwd-ws]",
		"message": []any{map[string]any{
			"type": "forward",
			"data": map[string]any{"id": "fwd-ws"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame["action"] != "get_forward_msg" {
		t.Fatalf("first action = %v", frame["action"])
	}
	params := frame["params"].(map[string]any)
	if params["id"] != "fwd-ws" && params["message_id"] != "fwd-ws" {
		t.Fatalf("params = %#v", params)
	}
	if err := conn.WriteJSON(map[string]any{
		"status":  "ok",
		"retcode": 0,
		"echo":    frame["echo"],
		"data": map[string]any{
			"messages": []any{
				map[string]any{
					"sender":      map[string]any{"user_id": 1, "nickname": "Alice"},
					"raw_message": "全文一",
					"message":     []any{map[string]any{"type": "text", "data": map[string]any{"text": "全文一"}}},
				},
				map[string]any{
					"sender":      map[string]any{"user_id": 2, "nickname": "Bob"},
					"raw_message": "全文二",
					"message":     []any{map[string]any{"type": "text", "data": map[string]any{"text": "全文二"}}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// No command match → ignored, but enrichment already happened; ensure no HTTP fallback noise.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			logText := logs.String()
			if !strings.Contains(logText, `reverse websocket message: message_type="private"`) {
				t.Fatalf("expected enriched message handling, logs=%q", logText)
			}
			if strings.Contains(logText, "get_forward_msg failed") {
				t.Fatalf("forward fetch failed over reverse ws: %q", logText)
			}
			_ = conn.Close()
			select {
			case <-handled:
			case <-time.After(time.Second):
			}
			return
		case <-time.After(50 * time.Millisecond):
			logText := logs.String()
			if strings.Contains(logText, `reverse websocket message: message_type="private"`) &&
				!strings.Contains(logText, "get_forward_msg failed") {
				_ = conn.Close()
				select {
				case <-handled:
				case <-time.After(time.Second):
				}
				return
			}
		}
	}
}
