package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
)

type deliveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn deliveryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestDeliveryAdapterContractMapsGroupTargetAndReply(t *testing.T) {
	var body sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/groups/g-1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "q-1", "timestamp": "2026-08-10T01:02:03Z"})
	}))
	defer server.Close()

	adapter := NewDeliveryAdapter(&Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "group", ID: "g-1"},
		ReplyTo: &message.ReplyRef{MessageID: "source", EventID: "event", Sequence: 3},
		Content: message.Content{Text: "hello"},
	})
	if outcome.State != delivery.OutcomeAccepted || outcome.Receipt.PlatformMessageID != "q-1" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if body.Content != "\n\nhello" || body.MsgID != "source" || body.EventID != "event" || body.MsgSeq != 3 {
		t.Fatalf("body = %#v", body)
	}
}

func TestDeliveryAdapterSupportsQQChannelAndGuildDirectTargets(t *testing.T) {
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "q-1"})
	}))
	defer server.Close()
	adapter := NewDeliveryAdapter(&Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()})
	for _, target := range []message.Conversation{
		{Platform: "qqbot", Type: "channel", ID: "channel-1"},
		{Platform: "qqbot", Type: "guild_private", ID: "guild-1"},
	} {
		outcome := adapter.Deliver(t.Context(), message.Outbound{Target: target, Content: message.Content{Text: "hello"}})
		if outcome.State != delivery.OutcomeAccepted {
			t.Fatalf("target=%#v outcome=%#v", target, outcome)
		}
	}
	want := []string{"/channels/channel-1/messages", "/dms/guild-1/messages"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestDeliveryAdapterContractPublishesPNGWithText(t *testing.T) {
	var uploadURL string
	var sent sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u-1/files":
			var body richMediaUploadRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			uploadURL = body.URL
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": map[string]any{"id": "media"}, "ttl": 60})
		case "/v2/users/u-1/messages":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "q-media"})
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewDeliveryAdapter(&Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "u-1"},
		Content: message.Content{
			Text:       "图片说明",
			Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("png")},
		},
	})
	if outcome.State != delivery.OutcomeAccepted || outcome.Receipt.PlatformMessageID != "q-media" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if uploadURL == "" || sent.Content != "图片说明" || sent.MsgType != 7 || sent.Media == nil {
		t.Fatalf("uploadURL=%q sent=%#v", uploadURL, sent)
	}
}

func TestDeliveryAdapterContractUsesURLAttachment(t *testing.T) {
	var uploaded richMediaUploadRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u/files":
			_ = json.NewDecoder(r.Body).Decode(&uploaded)
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": map[string]any{"id": "m"}, "ttl": 60})
		case "/v2/users/u/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		}
	}))
	defer server.Close()
	outcome := NewDeliveryAdapter(&Bot{BotToken: "t", APIBaseURL: server.URL, HTTPClient: server.Client()}).Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "u"},
		Content: message.Content{Attachment: &message.Attachment{URL: "https://cdn.example/x.png"}},
	})
	if outcome.State != delivery.OutcomeAccepted || uploaded.URL != "https://cdn.example/x.png" {
		t.Fatalf("outcome=%#v uploaded=%#v", outcome, uploaded)
	}
}

func TestDeliveryAdapterErrorClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		bot    *Bot
		out    message.Outbound
		want   delivery.OutcomeState
	}{
		{name: "wrong platform", out: message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "u"}, Content: message.Content{Text: "x"}}, want: delivery.OutcomeRejected},
		{name: "rate limited", status: http.StatusTooManyRequests, want: delivery.OutcomeRetryable},
		{name: "bad target", status: http.StatusBadRequest, want: delivery.OutcomeRejected},
		{name: "incomplete success", status: http.StatusOK, body: `{}`, want: delivery.OutcomeUnknown},
		{name: "credentials absent before send", bot: &Bot{}, want: delivery.OutcomeRejected},
		{
			name: "transport uncertain after send",
			bot: &Bot{BotToken: "token", APIBaseURL: "https://qq.invalid", HTTPClient: &http.Client{Transport: deliveryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			})}},
			want: delivery.OutcomeUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := tt.bot
			var server *httptest.Server
			if bot == nil {
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tt.status != 0 {
						w.WriteHeader(tt.status)
					}
					if tt.body != "" {
						_, _ = w.Write([]byte(tt.body))
					}
				}))
				defer server.Close()
				bot = &Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}
			}
			outbound := tt.out
			if outbound.Target.Platform == "" {
				outbound = message.Outbound{Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "u"}, Content: message.Content{Text: "x"}}
			}
			if got := NewDeliveryAdapter(bot).Deliver(context.Background(), outbound); got.State != tt.want {
				t.Fatalf("outcome = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryAdapterRetriesMediaUploadFailureBeforeSend(t *testing.T) {
	messageRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u/files":
			http.Error(w, "busy", http.StatusTooManyRequests)
		case "/v2/users/u/messages":
			messageRequests++
		}
	}))
	defer server.Close()

	outcome := NewDeliveryAdapter(&Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}).Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "u"},
		Content: message.Content{Attachment: &message.Attachment{URL: "https://cdn.example/x.png"}},
	})
	if outcome.State != delivery.OutcomeRetryable || messageRequests != 0 {
		t.Fatalf("outcome=%#v messageRequests=%d", outcome, messageRequests)
	}
}
