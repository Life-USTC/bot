package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAutoApproveFriendRequest(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/set_friend_add_request" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
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
	if gotBody["flag"] != "flag-1" || gotBody["approve"] != true {
		t.Fatalf("body = %#v", gotBody)
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
	bridge.handleIncomingEvent(context.Background(), raw, func(_ context.Context, event messageEvent) {
		dispatched = event
	})
	if !strings.Contains(dispatched.RawMessage, "Alice: 第一段") || !strings.Contains(dispatched.RawMessage, "Bob: 第二段") {
		t.Fatalf("raw = %q", dispatched.RawMessage)
	}
}

func TestProjectBusStyleForwardIDsFromCQ(t *testing.T) {
	ids := forwardIDsFromCQMessage("[CQ:forward,id=abc123]")
	if len(ids) != 1 || ids[0] != "abc123" {
		t.Fatalf("ids = %#v", ids)
	}
}
