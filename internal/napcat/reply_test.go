package napcat

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/message"
)

func TestReplySegmentsNormalizeWithoutMediaDiagnostics(t *testing.T) {
	structured := []any{
		map[string]any{"type": "reply", "data": map[string]any{"id": "456"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "周日呢"}},
	}
	for _, tt := range []struct {
		name      string
		event     messageEvent
		text      string
		forwarded bool
		fetched   bool
	}{
		{name: "structured", text: "[回复]周日呢", event: messageEvent{Message: structured}},
		{name: "flat with CQ reference", text: "周日呢", event: messageEvent{
			RawMessage: "[CQ:reply,id=456]周日呢",
			Message: []any{
				map[string]any{"type": "reply", "id": "456"},
				map[string]any{"type": "text", "data": map[string]any{"text": "周日呢"}},
			},
		}},
		{name: "CQ message", text: "周日呢", event: messageEvent{Message: "[CQ:reply,id=456]周日呢"}},
		{name: "CQ raw message", text: "周日呢", event: messageEvent{RawMessage: "[CQ:reply,id=456]周日呢"}},
		{name: "inline forward", forwarded: true, event: messageEvent{
			Message: []any{map[string]any{"type": "forward", "data": map[string]any{
				"content": []any{map[string]any{"type": "node", "data": map[string]any{
					"user_id": "7", "content": structured,
				}}},
			}}},
		}},
		{name: "fetched forward", forwarded: true, fetched: true, event: messageEvent{
			RawMessage: "[CQ:forward,id=forward-reference]",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !tt.fetched || r.URL.Path != "/get_forward_msg" {
					t.Errorf("unexpected action: %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[{"sender":{"user_id":"7"},"message":[{"type":"reply","data":{"id":"456"}},{"type":"text","data":{"text":"周日呢"}}]}]}}`))
			}))
			defer server.Close()
			var logs bytes.Buffer
			bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), Logger: log.New(&logs, "", 0)}
			event := tt.event
			bridge.enrichMessageEvent(t.Context(), &event)
			inbound := event.inbound()
			if len(inbound.Media) != 0 {
				t.Errorf("reply became attachment: %#v", inbound.Media)
			}
			if logs.Len() != 0 {
				t.Errorf("reply produced media diagnostics: %q", logs.String())
			}
			parts := inbound.Parts
			if tt.forwarded {
				if len(inbound.Forwarded) != 1 {
					t.Fatalf("forwarded messages = %#v", inbound.Forwarded)
				}
				parts = inbound.Forwarded[0].Parts
				if inbound.Forwarded[0].Text != "[回复]周日呢" {
					t.Errorf("forwarded text = %q", inbound.Forwarded[0].Text)
				}
				if inbound.ReplyTo != nil {
					t.Errorf("forwarded reply escaped to outer message: %#v", inbound.ReplyTo)
				}
			} else {
				if inbound.ReplyTo == nil || inbound.ReplyTo.MessageID != "456" {
					t.Errorf("reply reference = %#v", inbound.ReplyTo)
				}
				if inbound.Text != tt.text {
					t.Errorf("text = %q", inbound.Text)
				}
			}
			want := []message.InputPart{{Type: "reply", Text: "[回复]"}, {Type: "text", Text: "周日呢"}}
			if !reflect.DeepEqual(parts, want) {
				t.Errorf("parts = %#v, want %#v", parts, want)
			}
			wantCalls := int64(0)
			if tt.fetched {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Errorf("API calls = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestReplySegmentsPreserveAdjacentAttachments(t *testing.T) {
	event := messageEvent{Message: []any{
		map[string]any{"type": "reply", "data": map[string]any{"id": "456"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "请查看附件"}},
		map[string]any{"type": "image", "data": map[string]any{"url": "https://cdn.example/image.png"}},
		map[string]any{"type": "file", "data": map[string]any{"url": "https://cdn.example/file.pdf", "name": "file.pdf"}},
		map[string]any{"type": "future_attachment", "data": map[string]any{"id": "future-file"}},
	}}
	var logs bytes.Buffer
	bridge := &Bridge{Logger: log.New(&logs, "", 0)}
	bridge.enrichMessageEvent(t.Context(), &event)
	inbound := event.inbound()
	wantMedia := []message.InputMedia{
		{Kind: message.InputMediaImage, URL: "https://cdn.example/image.png"},
		{Kind: message.InputMediaFile, URL: "https://cdn.example/file.pdf", Name: "file.pdf", MIMEType: "application/pdf"},
		{Kind: message.InputMediaUnknown, FileID: "future-file"},
	}
	if !reflect.DeepEqual(inbound.Media, wantMedia) {
		t.Errorf("media = %#v, want %#v", inbound.Media, wantMedia)
	}
	if len(inbound.Parts) != 5 || inbound.Parts[0].Type != "reply" || inbound.Parts[0].Media != nil || inbound.Parts[1].Text != "请查看附件" {
		t.Errorf("reply/text parts = %#v", inbound.Parts)
	}
	if strings.Count(logs.String(), "napcat input media observation:") != 1 || !strings.Contains(logs.String(), "segment=future_attachment") || strings.Contains(logs.String(), "segment=reply") {
		t.Errorf("diagnostics = %q", logs.String())
	}
}
