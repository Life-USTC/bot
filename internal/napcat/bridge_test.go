package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/botapp"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
)

type processorSpy struct{ messages []message.Inbound }

func (s *processorSpy) Process(_ context.Context, inbound message.Inbound) {
	s.messages = append(s.messages, inbound)
}

func TestNapCatDelegatesInboundWithSeparateGroupActor(t *testing.T) {
	processor := &processorSpy{}
	bridge := &Bridge{App: processor}
	bridge.processInbound(context.Background(), messageEvent{
		MessageType: "group", GroupID: 99, UserID: 7, RawMessage: "hello", SelfID: 123,
	})
	if len(processor.messages) != 1 {
		t.Fatalf("processed messages = %d", len(processor.messages))
	}
	got := processor.messages[0]
	if got.Actor.UserID != "7" || got.Conversation.ID != "99" || got.Actor.UserID == got.Conversation.ID {
		t.Fatalf("inbound = %#v", got)
	}
}

func configureTestApp(t *testing.T, bridge *Bridge, handler commands.Handler, agentService *agent.Service, dispatcher *agent.Dispatcher, recorder botapp.Recorder) {
	t.Helper()
	deliverer, err := delivery.New(nil, NewDeliveryAdapter(bridge))
	if err != nil {
		t.Fatal(err)
	}
	var agentHandler botapp.AgentHandler
	if agentService != nil {
		agentHandler = agentService
	}
	var messageDispatcher botapp.Dispatcher
	if dispatcher != nil {
		messageDispatcher = dispatcher
	}
	app, err := botapp.New(botapp.Config{
		Commands: handler, Agent: agentHandler, Dispatcher: messageDispatcher, Delivery: deliverer,
		Recorder: recorder, Logger: bridge.Logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge.App = app
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
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

func TestForwardCachedImageReturnsSourceAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/forward_friend_single_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":null}`))
	}))
	defer server.Close()

	bridge := Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	receipt, err := bridge.forwardCachedImage(
		context.Background(),
		nil,
		nil,
		messageEvent{MessageType: "private", UserID: 456},
		"source-103",
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DeliveryMethod != store.DeliveryMethodForward ||
		receipt.SourceMessageID != "source-103" ||
		receipt.PlatformMessageID != "" ||
		receipt.AcceptedAt.IsZero() {
		t.Fatalf("receipt = %#v", receipt)
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
	var logs bytes.Buffer
	lifeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/courses" {
			t.Fatalf("unexpected Life @ USTC API path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer lifeServer.Close()

	bridge := &Bridge{Logger: log.New(&logs, "", 0)}
	configureTestApp(t, bridge, commands.Handler{
		Life: life.NewClient(lifeServer.URL, lifeServer.Client()), Prefix: "/life",
	}, nil, nil, nil)
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

	err = conn.WriteJSON(map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"raw_message":  "/life course calculus private-query",
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
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("reverse websocket handler did not stop")
	}
	if strings.Contains(logs.String(), "private-query") {
		t.Fatalf("logs contain private message text: %q", logs.String())
	}
	if !strings.Contains(logs.String(), `reverse websocket message: message_type="private" user_id=456 group_id=0`) {
		t.Fatalf("logs missing message metadata: %q", logs.String())
	}
}

func TestDispatchMessageBatchesAgentMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var modelRequests atomic.Int32
	var requestBody map[string]any
	replies := make(chan string, 1)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/send_private_msg" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			replies <- fmt.Sprint(body["message"])
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":1}}`))
			return
		}
		modelRequests.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-batch","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"合并完成"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`))
	}))
	defer modelServer.Close()
	agentService, err := agent.New(ctx, agent.Config{
		Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model",
	}, commands.Handler{}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{APIURL: modelServer.URL, HTTPClient: modelServer.Client()}
	dispatcher := agent.NewDispatcher(ctx, agentService, agent.DispatcherConfig{
		Debounce: 15 * time.Millisecond, MaxWait: 50 * time.Millisecond,
	})
	configureTestApp(t, bridge, commands.Handler{}, agentService, dispatcher, nil)
	bridge.processInbound(ctx, messageEvent{PostType: "message", MessageType: "private", RawMessage: "第一条", UserID: 42})
	bridge.processInbound(ctx, messageEvent{PostType: "message", MessageType: "private", RawMessage: "补充说明", UserID: 42})

	select {
	case reply := <-replies:
		if reply != "合并完成" {
			t.Fatalf("reply = %q", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batched reply")
	}
	if modelRequests.Load() != 1 {
		t.Fatalf("model requests = %d", modelRequests.Load())
	}
	select {
	case extra := <-replies:
		t.Fatalf("unexpected extra reply: %q", extra)
	case <-time.After(40 * time.Millisecond):
	}
	encoded, _ := json.Marshal(requestBody["messages"])
	if !bytes.Contains(encoded, []byte("第一条\\n\\n补充说明")) {
		t.Fatalf("model messages = %s", encoded)
	}
}

func TestDispatchMessageBypassesAgentQueueForCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agentService, err := agent.New(ctx, agent.Config{Enabled: false}, commands.Handler{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	replied := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replied <- struct{}{}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":1}}`))
	}))
	defer server.Close()
	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
	dispatcher := agent.NewDispatcher(ctx, agentService, agent.DispatcherConfig{
		Debounce: 200 * time.Millisecond, MaxWait: 300 * time.Millisecond,
	})
	configureTestApp(t, bridge, commands.Handler{Prefix: "/life"}, agentService, dispatcher, nil)
	bridge.processInbound(ctx, messageEvent{
		PostType: "message", MessageType: "private", RawMessage: "帮助", UserID: 42,
	})
	select {
	case <-replied:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("command waited for the agent batching window")
	}
}

func TestReverseBridgeRepliesOnMessageConnectionAfterNewerConnectionCloses(t *testing.T) {
	lifeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/courses" {
			t.Fatalf("unexpected Life @ USTC API path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer lifeServer.Close()

	bridge := &Bridge{}
	configureTestApp(t, bridge, commands.Handler{
		Life: life.NewClient(lifeServer.URL, lifeServer.Client()), Prefix: "/life",
	}, nil, nil, nil)
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
	if _, err := bridge.runOnce(context.Background()); err == nil {
		t.Fatal("runOnce returned nil after websocket close")
	}
	if auth := <-authHeader; auth != "Bearer token" {
		t.Fatalf("authorization = %q", auth)
	}
}

func TestEnqueueForwardEventAppliesBackpressureAtCapacity(t *testing.T) {
	events := make(chan messageEvent, forwardEventQueueSize)
	for id := int64(0); id < forwardEventQueueSize; id++ {
		if !enqueueForwardEvent(context.Background(), events, messageEvent{UserID: id}) {
			t.Fatalf("enqueue %d was unexpectedly canceled", id)
		}
	}
	if len(events) != cap(events) {
		t.Fatalf("queue length = %d, capacity = %d", len(events), cap(events))
	}
	done := make(chan bool, 1)
	go func() {
		done <- enqueueForwardEvent(context.Background(), events, messageEvent{UserID: 99})
	}()
	select {
	case <-done:
		t.Fatal("enqueue completed while the queue was full")
	case <-time.After(20 * time.Millisecond):
	}
	if got := (<-events).UserID; got != 0 {
		t.Fatalf("first queued user ID = %d", got)
	}
	select {
	case accepted := <-done:
		if !accepted {
			t.Fatal("enqueue after freeing capacity was canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue did not resume after freeing capacity")
	}
}

func TestEnqueueForwardEventCancellationReleasesBackpressure(t *testing.T) {
	events := make(chan messageEvent, 1)
	events <- messageEvent{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- enqueueForwardEvent(ctx, events, messageEvent{})
	}()
	cancel()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("enqueue succeeded after cancellation with a full queue")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue did not stop after cancellation")
	}
}

func TestRunOnceCancellationStopsConnectionAndWorkers(t *testing.T) {
	upgrader := websocket.Upgrader{}
	connected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		close(connected)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	bridge := &Bridge{WSURL: "ws" + server.URL[len("http"):]}
	go func() {
		_, err := bridge.runOnce(ctx)
		done <- err
	}()
	<-connected
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runOnce did not stop after cancellation")
	}
}

func TestRunReconnectsAfterRepeatedDisconnects(t *testing.T) {
	var connections atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if connections.Add(1) >= 3 {
			cancel()
		}
		_ = conn.Close()
	}))
	defer server.Close()

	bridge := &Bridge{WSURL: "ws" + server.URL[len("http"):]}
	err := bridge.run(ctx, retry.Backoff{Initial: time.Millisecond, Max: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got := connections.Load(); got < 3 {
		t.Fatalf("connections = %d, want at least 3", got)
	}
}

func TestHandleMessageRecordsIgnored(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	bridge := &Bridge{}
	configureTestApp(t, bridge, commands.Handler{Store: db, Prefix: "/life"}, nil, nil, db)
	bridge.processInbound(context.Background(), messageEvent{
		PostType:    "message",
		MessageType: "private",
		RawMessage:  "not a command",
		UserID:      456,
	})
	count, err := db.InteractionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestMessageEventExtractsStructuredAndCQImages(t *testing.T) {
	structured := messageEvent{Message: []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "看看"}},
		map[string]any{"type": "image", "data": map[string]any{"url": "https://cdn.example/image.png"}},
	}}
	if got := structured.imageURLs(); len(got) != 1 || got[0] != "https://cdn.example/image.png" {
		t.Fatalf("structured image URLs = %#v", got)
	}
	cq := messageEvent{RawMessage: "[CQ:image,file=https://cdn.example/fallback.jpg?x=1&amp;y=2]"}
	if got := cq.imageURLs(); len(got) != 1 || got[0] != "https://cdn.example/fallback.jpg?x=1&y=2" {
		t.Fatalf("CQ image URLs = %#v", got)
	}
}

func TestInvalidInboundIsRejectedBeforeRecording(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var logs bytes.Buffer
	bridge := &Bridge{
		Logger: log.New(&logs, "", 0),
	}
	configureTestApp(t, bridge, commands.Handler{Store: db, Prefix: "/life"}, nil, nil, db)
	bridge.processInbound(context.Background(), messageEvent{
		PostType:   "message",
		RawMessage: "not a command",
		UserID:     456,
	})
	if !strings.Contains(logs.String(), "reject inbound message: inbound conversation is incomplete") {
		t.Fatalf("logs = %q", logs.String())
	}
}
