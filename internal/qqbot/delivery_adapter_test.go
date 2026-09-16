package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
)

type deliveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn deliveryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func testPNG(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

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
		Content: message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
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
		outcome := adapter.Deliver(t.Context(), message.Outbound{Target: target, Content: message.Content{Parts: []message.ContentPart{{Text: "hello"}}}})
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
	pngData := testPNG(t)
	var prepared richMediaPrepareRequest
	var uploaded []byte
	var sent sendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u-1/upload_prepare":
			if err := json.NewDecoder(r.Body).Decode(&prepared); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id":     "upload-1",
				"block_size":    len(pngData),
				"parts":         []map[string]any{{"index": 0, "presigned_url": server.URL + "/part/0", "block_size": len(pngData)}},
				"upload_config": map[string]any{"concurrency": 1, "retry_timeout": 1, "retry_delay": 0},
			})
		case "/part/0":
			var err error
			uploaded, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
		case "/v2/users/u-1/upload_part_finish":
			var body richMediaPartFinishRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.UploadID != "upload-1" || body.PartIndex != 0 || body.BlockSize != strconv.Itoa(len(pngData)) || body.MD5 == "" {
				t.Fatalf("finish body = %#v", body)
			}
		case "/v2/users/u-1/files":
			var body richMediaUploadRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.UploadID != "upload-1" || body.FileType != 1 || body.SrvSendMsg {
				t.Fatalf("merge body = %#v", body)
			}
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
	})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "u-1"},
		Content: message.Content{Parts: []message.ContentPart{{Text: "图片说明",
			Attachment: &message.Attachment{MIMEType: "image/png", Data: pngData}}},
		},
	})
	if outcome.State != delivery.OutcomeAccepted || outcome.Receipt.PlatformMessageID != "q-media" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !bytes.Equal(uploaded, pngData) || prepared.FileSize != strconv.Itoa(len(pngData)) || prepared.FileName != qqRichMediaFileName || prepared.MD5 == "" || prepared.SHA1 == "" || prepared.MD510M == "" {
		t.Fatalf("prepared=%#v uploaded=%d", prepared, len(uploaded))
	}
	if sent.Content != "图片说明" || sent.MsgType != 7 || sent.Media == nil {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestDeliveryAdapterContractUsesURLAttachment(t *testing.T) {
	pngData := testPNG(t)
	var uploaded []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/source.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		case "/v2/users/u/upload_prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id":  "upload-url",
				"block_size": len(pngData),
				"parts":      []map[string]any{{"index": 0, "presigned_url": server.URL + "/url-part", "block_size": len(pngData)}},
			})
		case "/url-part":
			uploaded, _ = io.ReadAll(r.Body)
		case "/v2/users/u/upload_part_finish":
		case "/v2/users/u/files":
			var body richMediaUploadRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.UploadID != "upload-url" {
				t.Fatalf("merge body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": map[string]any{"id": "m"}, "ttl": 60})
		case "/v2/users/u/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		}
	}))
	defer server.Close()
	outcome := NewDeliveryAdapter(&Bot{BotToken: "t", APIBaseURL: server.URL, HTTPClient: server.Client()}).Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "u"},
		Content: message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{URL: server.URL + "/source.png"}}}},
	})
	if outcome.State != delivery.OutcomeAccepted || !bytes.Equal(uploaded, pngData) {
		t.Fatalf("outcome=%#v uploaded=%d", outcome, len(uploaded))
	}
}

func TestDeliveryAdapterDoesNotSendImageAltTextAsCaption(t *testing.T) {
	pngData := testPNG(t)
	var sent sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u/upload_prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id":  "upload-alt",
				"block_size": len(pngData),
				"parts":      []map[string]any{{"index": 0, "presigned_url": "http://upload.invalid/part", "block_size": len(pngData)}},
			})
		case "/v2/users/u/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": map[string]any{"id": "m"}, "ttl": 60})
		case "/v2/users/u/messages":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		}
	}))
	defer server.Close()

	adapter := NewDeliveryAdapter(&Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: &http.Client{Transport: deliveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "upload.invalid" {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(pngData)), Header: make(http.Header), Request: req}, nil
			}
			return server.Client().Transport.RoundTrip(req)
		})},
	})
	outcome := adapter.Deliver(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "u"},
		Content: message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{
			MIMEType: "image/png", Data: pngData, AltText: "卡片说明",
		}}}},
	})
	if outcome.State != delivery.OutcomeAccepted || sent.Content != "" || sent.Media == nil {
		t.Fatalf("outcome=%#v sent=%#v", outcome, sent)
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
		{name: "wrong platform", out: message.Outbound{Target: message.Conversation{Platform: "napcat", Type: "private", ID: "u"}, Content: message.Content{Parts: []message.ContentPart{{Text: "x"}}}}, want: delivery.OutcomeRejected},
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
				outbound = message.Outbound{Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "u"}, Content: message.Content{Parts: []message.ContentPart{{Text: "x"}}}}
			}
			if got := NewDeliveryAdapter(bot).Deliver(context.Background(), outbound); got.State != tt.want {
				t.Fatalf("outcome = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryAdapterRetriesMediaUploadFailureBeforeSend(t *testing.T) {
	pngData := testPNG(t)
	messageRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/u/upload_prepare":
			http.Error(w, "busy", http.StatusTooManyRequests)
		case "/v2/users/u/messages":
			messageRequests++
		}
	}))
	defer server.Close()

	outcome := NewDeliveryAdapter(&Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}).Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "u"},
		Content: message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{MIMEType: "image/png", Data: pngData}}}},
	})
	if outcome.State != delivery.OutcomeRetryable || messageRequests != 0 {
		t.Fatalf("outcome=%#v messageRequests=%d", outcome, messageRequests)
	}
}
