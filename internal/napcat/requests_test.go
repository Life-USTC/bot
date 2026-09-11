package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAutoApproveRequestsOverReverseWebSocket(t *testing.T) {
	for _, tc := range []struct{ name, kind, subtype, action string }{
		{"friend", "friend", "", "set_friend_add_request"},
		{"group invitation", "group", "invite", "set_group_add_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bridge := &Bridge{}
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer close(done)
				bridge.handleReverseConn(ctx, conn)
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = conn.Close(); <-done }()
			if err := conn.WriteJSON(requestEvent{PostType: "request", RequestType: tc.kind, SubType: tc.subtype, Flag: "flag-123", UserID: 7, GroupID: 99}); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var frame struct {
				Action string         `json:"action"`
				Params map[string]any `json:"params"`
				Echo   any            `json:"echo"`
			}
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if frame.Action != tc.action || frame.Params["flag"] != "flag-123" || frame.Params["approve"] != true {
				t.Fatalf("action = %#v", frame)
			}
			if tc.kind == "group" && frame.Params["sub_type"] != "invite" {
				t.Fatalf("params = %#v", frame.Params)
			}
			if err := conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": frame.Echo}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGroupInvitationHTTPAndRejectedAction(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[rejected], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var params map[string]any
				if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/set_group_add_request" || params["flag"] != "flag-1" || params["approve"] != true || params["sub_type"] != "invite" {
					t.Errorf("request = %s %#v", r.URL.Path, params)
				}
				if rejected {
					_, _ = w.Write([]byte(`{"status":"failed","retcode":1,"message":"denied"}`))
				} else {
					_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
				}
			}))
			defer server.Close()
			var logs bytes.Buffer
			bridge := &Bridge{APIURL: server.URL, Logger: log.New(&logs, "", 0)}
			bridge.handleRequestEvent(context.Background(), requestEvent{RequestType: "group", SubType: "invite", Flag: " flag-1 ", GroupID: 99})
			if strings.Contains(logs.String(), "auto-approved group invitation") == rejected {
				t.Fatalf("log = %s", logs.String())
			}
		})
	}
}

func TestIgnoreUnrelatedOrMalformedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected action: %s", r.URL.Path) }))
	defer server.Close()
	bridge := &Bridge{APIURL: server.URL}
	for _, event := range []requestEvent{
		{RequestType: "group", SubType: "add", Flag: "valid"},
		{RequestType: "group", SubType: "invite", Flag: " "},
		{RequestType: "group", Flag: "valid"},
		{RequestType: "friend", Flag: " "},
		{RequestType: "unknown", Flag: "valid"},
	} {
		bridge.handleRequestEvent(context.Background(), event)
	}
}
