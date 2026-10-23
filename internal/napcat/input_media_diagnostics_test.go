package napcat

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/message"
)

func TestUnknownMediaDiagnosticsPreserveInputAndHideValues(t *testing.T) {
	unknown := map[string]any{"type": "future_attachment", "data": map[string]any{
		"url": "https://private.example/document?token=secret-token", "file_id": "private-file-id",
		"name": "private-document", "content_type": "", "file_size": float64(42),
		"caption": "private-caption", "secret-field": "private-value",
	}}
	flat := map[string]any{"type": "future_attachment"}
	for key, value := range unknown["data"].(map[string]any) {
		flat[key] = value
	}
	for _, tt := range []struct {
		name    string
		payload any
		source  string
		layout  string
	}{
		{"segments", []any{unknown}, "message", "data"},
		{"flat segment", []any{flat}, "message", "flat"},
		{"inline forward", []any{map[string]any{"type": "forward", "data": map[string]any{
			"content": []any{map[string]any{"type": "node", "data": map[string]any{
				"user_id": "private-user", "content": []any{unknown},
			}}},
		}}}, "message.forward", "data"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			bridge := &Bridge{Logger: log.New(&logs, "", 0)}
			event := messageEvent{Message: tt.payload}
			before := event.inbound().Media
			bridge.enrichMessageEvent(t.Context(), &event)
			if !reflect.DeepEqual(event.inbound().Media, before) {
				t.Fatalf("diagnostics changed media: before=%#v after=%#v", before, event.inbound().Media)
			}
			if len(before) != 1 || before[0].Kind != message.InputMediaUnknown {
				t.Fatalf("fixture did not retain unknown media: %#v", before)
			}
			for _, want := range []string{"source=" + tt.source, "layout=" + tt.layout, "segment=future_attachment", "kind=unknown", "content_type=empty-string", "file_size=number", "url=string", "file_id=string"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("missing %q in %q", want, logs.String())
				}
			}
			for _, secret := range []string{"private", "secret", "https://", "caption"} {
				if strings.Contains(logs.String(), secret) {
					t.Errorf("diagnostics leaked %q: %q", secret, logs.String())
				}
			}
		})
	}
}

func TestUnknownMediaDiagnosticsObserveForwardResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_forward_msg" {
			t.Errorf("unexpected action: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[{"sender":{"user_id":"private-user"},"message":[{"type":"text","data":{"text":"please inspect"}},{"type":"future_attachment","data":{"url":"https://private.example/file","mime_type":{}}}]}]}}`))
	}))
	defer server.Close()
	var logs bytes.Buffer
	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), Logger: log.New(&logs, "", 0)}
	event := messageEvent{RawMessage: "[CQ:forward,id=private-forward]"}
	bridge.enrichMessageEvent(t.Context(), &event)
	media := event.inbound().Media
	if len(media) != 1 || media[0].Kind != message.InputMediaUnknown || media[0].URL != "https://private.example/file" {
		t.Fatalf("media=%#v", media)
	}
	for _, want := range []string{"source=forward-response", "segment=future_attachment", "mime_type=object"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in %q", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "private") {
		t.Fatalf("diagnostics leaked input: %q", logs.String())
	}
}

func TestUnknownMediaDiagnosticsDoNotChangeKnownAttachments(t *testing.T) {
	for _, payload := range []any{
		[]any{
			map[string]any{"type": "image", "data": map[string]any{"url": "https://cdn.example/image.png"}},
			map[string]any{"type": "file", "data": map[string]any{"url": "https://cdn.example/file.pdf", "name": "file.pdf"}},
		},
		"[CQ:image,url=https://cdn.example/image.png][CQ:file,url=https://cdn.example/file.pdf,name=file.pdf]",
	} {
		var logs bytes.Buffer
		bridge := &Bridge{Logger: log.New(&logs, "", 0)}
		event := messageEvent{Message: payload}
		before := event.inbound().Media
		bridge.enrichMessageEvent(t.Context(), &event)
		if len(before) != 2 || !reflect.DeepEqual(event.inbound().Media, before) {
			t.Fatalf("known media changed: before=%#v after=%#v", before, event.inbound().Media)
		}
		if logs.Len() != 0 {
			t.Errorf("valid media should not produce diagnostic noise: %q", logs.String())
		}
	}
}

func TestUnknownMediaDiagnosticsBoundUntrustedTypesAndVolume(t *testing.T) {
	var segments []any
	for range 40 {
		segments = append(segments, map[string]any{"type": "private\nforged-log " + strings.Repeat("x", 1000), "data": map[string]any{
			"url": "https://private.example/file", "mime_type": []any{"private-value"},
		}})
	}
	var logs bytes.Buffer
	bridge := &Bridge{Logger: log.New(&logs, "", 0)}
	event := messageEvent{Message: segments}
	bridge.enrichMessageEvent(t.Context(), &event)
	if !strings.Contains(logs.String(), "segment=invalid") || !strings.Contains(logs.String(), "mime_type=array") {
		t.Fatalf("missing sanitized type observations: %q", logs.String())
	}
	if strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "forged") || logs.Len() > 4096 || strings.Count(logs.String(), "\n") > 9 {
		t.Fatalf("diagnostics not bounded and sanitized: %q", logs.String())
	}
}
