package napcat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
)

func TestDeliveryAdapterSendsInlinePNGAndMapsPrivateTarget(t *testing.T) {
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
	})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Content: message.Content{Parts: []message.ContentPart{{Text: "课表",
			Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("png")}}},
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
	if textData["text"] != "课表" || imageData["file"] != "base64://"+base64.StdEncoding.EncodeToString([]byte("png")) {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestDeliveryAdapterContractMapsGroupAndURLAttachment(t *testing.T) {
	for _, kind := range []string{"private", "group"} {
		t.Run(kind, func(t *testing.T) {
			png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("room map")...)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(png) }))
			defer upstream.Close()
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/send_"+kind+"_msg" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":8}}`))
			}))
			defer server.Close()
			adapter := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()})
			outcome := adapter.Deliver(t.Context(), message.Outbound{
				Target:  message.Conversation{Platform: "napcat", Type: kind, ID: "100"},
				Content: message.Content{Parts: []message.ContentPart{{Text: "3C101：三教副 1", Attachment: &message.Attachment{URL: upstream.URL + "/3C101.png"}}}},
			})
			if outcome.State != delivery.OutcomeAccepted {
				t.Fatalf("outcome=%#v", outcome)
			}
			segments := body["message"].([]any)
			if len(segments) != 2 {
				t.Fatalf("segments=%v", segments)
			}
			url := segments[1].(map[string]any)["data"].(map[string]any)["file"].(string)
			data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "base64://"))
			if err != nil || !strings.HasPrefix(url, "base64://") || string(data) != string(png) {
				t.Fatalf("inline image differs: %v", err)
			}

		})
	}
}

func TestRemoteImageDownloadFailureDoesNotSendMessage(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"upstream failure", 503, "unavailable"}, {"not PNG", 200, "<html>error</html>"},
		{"too large", 200, "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 10<<20)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("sent message despite download failure")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			adapter := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()})
			outcome := adapter.Deliver(t.Context(), message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "42"}, Content: message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{URL: server.URL}}}}})
			if outcome.State != delivery.OutcomeRetryable || outcome.Code != "attachment_unavailable" {
				t.Fatalf("outcome=%#v", outcome)
			}
		})
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
		Content: message.Content{Parts: []message.ContentPart{{Text: "查询结果"}}},
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
			out: message.Outbound{Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "1"}, Content: message.Content{Parts: []message.ContentPart{{Text: "x"}}}},
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
				outbound = message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "1"}, Content: message.Content{Parts: []message.ContentPart{{Text: "x"}}}}
			}
			if got := NewDeliveryAdapter(bridge).Deliver(context.Background(), outbound); got.State != tt.want {
				t.Fatalf("outcome = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryAdapterSendsAllBundlePartsInOneRequest(t *testing.T) {
	calls := 0
	var body struct {
		Message []struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		} `json:"message"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":"bundle"}}`))
	}))
	defer server.Close()
	adapter := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()})
	outcome := adapter.Deliver(t.Context(), message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "42"}, Content: message.Content{Parts: []message.ContentPart{
		{Text: "说明"}, {Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("a")}}, {Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("b")}}, {Text: "#校车（成功）"},
	}}})
	if outcome.State != delivery.OutcomeAccepted || calls != 1 || len(body.Message) != 4 {
		t.Fatalf("outcome=%#v calls=%d payload=%#v", outcome, calls, body)
	}
	for index, kind := range []string{"text", "image", "image", "text"} {
		if body.Message[index].Type != kind {
			t.Fatalf("order=%#v", body)
		}
	}
	if body.Message[3].Data["text"] != "#校车（成功）" {
		t.Fatalf("receipt=%#v", body.Message[3])
	}
}
