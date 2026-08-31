package napcat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
)

func TestDeliveryAdapterContractPublishesPNGAndMapsPrivateTarget(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":"n-1"}}`))
	}))
	defer server.Close()

	adapter := NewDeliveryAdapter(&Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Content: message.Content{
			Text:       "课表",
			Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("png")},
		},
	})
	if outcome.State != delivery.OutcomeAccepted || outcome.Receipt.PlatformMessageID != "n-1" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if body["user_id"] != float64(42) {
		t.Fatalf("body = %#v", body)
	}
	segments, ok := body["message"].([]any)
	if !ok || len(segments) != 2 {
		t.Fatalf("message = %#v", body["message"])
	}
	textData := segments[0].(map[string]any)["data"].(map[string]any)
	imageData := segments[1].(map[string]any)["data"].(map[string]any)
	if textData["text"] != "课表" || !strings.HasPrefix(imageData["file"].(string), server.URL+"/media/") {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestDeliveryAdapterContractMapsGroupAndURLAttachment(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":8}}`))
	}))
	defer server.Close()

	adapter := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "napcat", Type: "group", ID: "100"},
		Content: message.Content{Attachment: &message.Attachment{URL: "https://cdn.example/x.png"}},
	})
	if outcome.State != delivery.OutcomeAccepted || body["group_id"] != float64(100) {
		t.Fatalf("outcome=%#v body=%#v", outcome, body)
	}
}

func TestDeliveryAdapterSendsActualNapCatReplySegment(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":9}}`))
	}))
	defer server.Close()

	outcome := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()}).Deliver(t.Context(), message.Outbound{
		Target:  message.Conversation{Platform: "napcat", Type: "group", ID: "100"},
		ReplyTo: &message.ReplyRef{MessageID: "456"},
		Content: message.Content{Text: "查询结果"},
	})
	if outcome.State != delivery.OutcomeAccepted {
		t.Fatalf("outcome = %#v", outcome)
	}
	segments, ok := body["message"].([]any)
	if !ok || len(segments) != 2 {
		t.Fatalf("message = %#v", body["message"])
	}
	reply := segments[0].(map[string]any)
	data := reply["data"].(map[string]any)
	if reply["type"] != "reply" || data["id"] != "456" {
		t.Fatalf("reply segment = %#v", reply)
	}
}

func TestDeliveryAdapterErrorClassification(t *testing.T) {
	tests := []struct {
		name    string
		bridge  func(*httptest.Server) *Bridge
		handler http.HandlerFunc
		out     message.Outbound
		want    delivery.OutcomeState
	}{
		{
			name: "wrong platform", want: delivery.OutcomeRejected,
			out: message.Outbound{Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "1"}, Content: message.Content{Text: "x"}},
		},
		{
			name: "rate limited", want: delivery.OutcomeRetryable,
			handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusTooManyRequests) },
		},
		{
			name: "platform rejection", want: delivery.OutcomeRejected,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"status":"failed","retcode":1200,"message":"bad target"}`))
			},
		},
		{
			name: "incomplete success", want: delivery.OutcomeUnknown,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
			},
		},
		{
			name: "transport uncertain", want: delivery.OutcomeUnknown,
			bridge: func(_ *httptest.Server) *Bridge {
				return &Bridge{APIURL: "https://napcat.invalid", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("connection reset")
				})}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *httptest.Server
			if tt.handler != nil {
				server = httptest.NewServer(tt.handler)
				defer server.Close()
			}
			bridge := &Bridge{}
			if server != nil {
				bridge = &Bridge{APIURL: server.URL, HTTPClient: server.Client()}
			}
			if tt.bridge != nil {
				bridge = tt.bridge(server)
			}
			outbound := tt.out
			if outbound.Target.Platform == "" {
				outbound = message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "1"}, Content: message.Content{Text: "x"}}
			}
			if got := NewDeliveryAdapter(bridge).Deliver(context.Background(), outbound); got.State != tt.want {
				t.Fatalf("outcome = %#v, want %q", got, tt.want)
			}
		})
	}
}
