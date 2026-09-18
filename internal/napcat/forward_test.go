package napcat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/message"
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

func TestFriendRequestTipMessageIsNotDispatched(t *testing.T) {
	var mu sync.Mutex
	var flags []string
	done := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set_friend_add_request":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			mu.Lock()
			flags = append(flags, fmt.Sprintf("%v", body["flag"]))
			mu.Unlock()
			if fmt.Sprintf("%v", body["flag"]) == "1002" {
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
				once.Do(func() { close(done) })
				return
			}
			_, _ = w.Write([]byte(`{"status":"failed","retcode":1,"message":"No such request"}`))
		case "/get_doubt_friends_add_request":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), Logger: log.New(io.Discard, "", 0)}
	raw, _ := json.Marshal(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"user_id":      42,
		"time":         1000,
		"raw_message":  "请求添加你为好友",
		"message":      []any{},
	})
	dispatched := false
	bridge.handleIncomingEvent(context.Background(), raw, func(context.Context, messageEvent) {
		dispatched = true
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tip approve")
	}
	if dispatched {
		t.Fatal("friend request tip should not dispatch as chat")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(flags) == 0 || flags[len(flags)-1] != "1002" {
		t.Fatalf("flags = %#v", flags)
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

func TestEnrichForwardMessagePreservesStructuredSpeakerTimeNestedMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_forward_msg" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[
			{"time":1720000000,"sender":{"user_id":"1001","nickname":"Alice"},"message":[
				{"type":"text","data":{"text":"请看附件"}},
				{"type":"image","data":{"url":"https://cdn.example/a.png"}},
				{"type":"mface","data":{"url":"https://cdn.example/sticker.gif","name":"开心"}},
				{"type":"file","data":{"url":"https://cdn.example/report.pdf","name":"report.pdf","file_id":"file-1"}},
				{"type":"forward","data":{"content":[
					{"type":"node","data":{"time":1720000060,"user_id":"1002","nickname":"Bob","content":[{"type":"text","data":{"text":"嵌套内容"}}]}}
				]}}
			]},
			{"time":"2026-07-03T12:00:00Z","sender":{"user_id":"1003","card":"Carol"},"raw_message":"第二条","message":[{"type":"text","data":{"text":"第二条"}}]}
		]}}`))
	}))
	defer server.Close()

	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	event := messageEvent{
		MessageType: "private",
		RawMessage:  "[CQ:forward,id=structured-fwd]",
		Message:     []any{map[string]any{"type": "forward", "data": map[string]any{"id": "structured-fwd"}}},
	}
	bridge.enrichMessageEvent(context.Background(), &event)
	inbound := event.inbound()
	if len(inbound.Forwarded) != 2 {
		t.Fatalf("forwarded = %#v", inbound.Forwarded)
	}
	first := inbound.Forwarded[0]
	if first.Speaker.UserID != "1001" || first.Speaker.DisplayName != "Alice" || first.SentAt.Unix() != 1720000000 {
		t.Fatalf("first forwarded metadata = %#v", first)
	}
	if len(first.Parts) < 4 {
		t.Fatalf("first parts = %#v", first.Parts)
	}
	if first.Parts[1].Media == nil || first.Parts[1].Media.Kind != message.InputMediaImage || first.Parts[1].Media.URL != "https://cdn.example/a.png" {
		t.Fatalf("image part = %#v", first.Parts[1])
	}
	if first.Parts[2].Media == nil || first.Parts[2].Media.Kind != message.InputMediaSticker || first.Parts[2].Media.URL != "https://cdn.example/sticker.gif" {
		t.Fatalf("sticker part = %#v", first.Parts[2])
	}
	if first.Parts[3].Media == nil || first.Parts[3].Media.Kind != message.InputMediaFile || first.Parts[3].Media.Name != "report.pdf" {
		t.Fatalf("file part = %#v", first.Parts[3])
	}
	if first.Parts[4].Forward == nil || first.Parts[4].Forward.Speaker.UserID != "1002" || first.Parts[4].Forward.Text != "嵌套内容" {
		t.Fatalf("nested forward = %#v", first.Parts[4])
	}
	if len(inbound.Media) != 3 {
		t.Fatalf("media index = %#v", inbound.Media)
	}
	if len(event.imageURLs()) != 2 {
		t.Fatalf("image urls = %#v", event.imageURLs())
	}
}

func TestNapCatInboundCapturesStickerURLFromCQ(t *testing.T) {
	event := messageEvent{
		MessageType: "private",
		RawMessage:  "[CQ:mface,emoji_id=42,url=https://cdn.example/meme.gif]",
		Message:     []any{map[string]any{"type": "mface", "data": map[string]any{"emoji_id": "42", "url": "https://cdn.example/meme.gif"}}},
	}
	inbound := event.inbound()
	if len(inbound.Media) != 1 || inbound.Media[0].Kind != message.InputMediaSticker || inbound.Media[0].URL != "https://cdn.example/meme.gif" {
		t.Fatalf("sticker media = %#v", inbound.Media)
	}
	if len(inbound.Parts) != 1 || inbound.Parts[0].Media == nil || inbound.Parts[0].Media.Kind != message.InputMediaSticker {
		t.Fatalf("sticker parts = %#v", inbound.Parts)
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

func TestFormatForwardMessagePreservesLargePayload(t *testing.T) {
	nodes := make([]any, 0, 205)
	for i := 0; i < 205; i++ {
		nodes = append(nodes, map[string]any{
			"data": map[string]any{
				"nickname": "U",
				"content":  []any{map[string]any{"type": "text", "data": map[string]any{"text": strings.Repeat("x", 20)}}},
			},
		})
	}
	raw, _ := json.Marshal(map[string]any{"messages": nodes})
	got := formatForwardMessageData(raw)
	if strings.Contains(got, "合并转发已截断") || strings.Count(got, strings.Repeat("x", 20)) != 205 {
		t.Fatalf("forwarded content lost: %q", got)
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
		Logger: log.New(&logs, "", 0),
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

func TestNestedForwardReferenceAndFileIDAreResolvedBeforeAdmission(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path == "/get_file" {
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"url":"https://cdn.example/report.txt"}}`))
			return
		}
		var params map[string]string
		_ = json.NewDecoder(r.Body).Decode(&params)
		if params["message_id"] == "outer" {
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[{"time":1720000000,"sender":{"user_id":"1001","nickname":"Alice"},"message":[{"type":"forward","data":{"id":"inner"}}]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[{"time":1720000001,"sender":{"user_id":"1002","nickname":"Bob"},"message":[{"type":"text","data":{"text":"校车 [CQ:at,qq=42]"}},{"type":"file","data":{"file_id":"file-reference","name":"report.txt"}}]}]}}`))
	}))
	defer server.Close()
	event := messageEvent{MessageType: "private", UserID: 1000, SelfID: 42, RawMessage: "请总结 [CQ:forward,id=outer]", Message: []any{map[string]any{"type": "text", "data": map[string]any{"text": "请总结 "}}, map[string]any{"type": "forward", "data": map[string]any{"id": "outer"}}}}
	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	bridge.enrichMessageEvent(t.Context(), &event)
	inbound := event.inbound()
	if inbound.BotMentioned || strings.Contains(inbound.Text, "校车") {
		t.Fatalf("forwarded text affected outer routing: %#v", inbound)
	}
	if calls["/get_forward_msg"] != 2 || calls["/get_file"] != 1 {
		t.Fatalf("calls=%v", calls)
	}
	if len(inbound.Media) != 1 || inbound.Media[0].URL != "https://cdn.example/report.txt" {
		t.Fatalf("media=%#v", inbound.Media)
	}
	if len(inbound.Forwarded) != 1 || len(inbound.Forwarded[0].Parts) != 1 || inbound.Forwarded[0].Parts[0].Forward == nil || inbound.Forwarded[0].Parts[0].Forward.Speaker.UserID != "1002" {
		t.Fatalf("nested=%#v", inbound.Forwarded)
	}
}

func TestStructuredOuterMentionIsIndependentOfForwardedMentions(t *testing.T) {
	for _, addressed := range []bool{false, true} {
		var segments []any
		if err := json.Unmarshal([]byte(`[{"type":"forward","data":{"content":[{"sender":{"nickname":"Alice"},"message":[{"type":"text","data":{"text":"校车 [CQ:at,qq=42]"}}]}]}}]`), &segments); err != nil {
			t.Fatal(err)
		}
		if addressed {
			segments = append([]any{map[string]any{"type": "at", "data": map[string]any{"qq": "42"}}}, segments...)
		}
		event := messageEvent{MessageType: "group", UserID: 1000, GroupID: 2000, SelfID: 42, Message: segments}
		(&Bridge{}).enrichMessageEvent(t.Context(), &event)
		inbound := event.inbound()
		if inbound.BotMentioned != addressed || inbound.Text != "" {
			t.Fatalf("addressed=%v inbound=%#v", addressed, inbound)
		}
	}
}
