package napcat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fileEvent(fileID string) *messageEvent {
	return &messageEvent{
		MessageType: "private",
		UserID:      1000,
		SelfID:      42,
		Message: []any{map[string]any{
			"type": "file",
			"data": map[string]any{"file_id": fileID, "file": "report.docx", "file_size": "58285"},
		}},
	}
}

func getFileServer(t *testing.T, data map[string]any, calls map[string]int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path != "/get_file" {
			w.WriteHeader(404)
			return
		}
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Errorf("get_file params undecodable: %v", err)
		}
		if params["file_id"] != "file-reference" {
			t.Errorf("get_file file_id=%v", params["file_id"])
		}
		payload, err := json.Marshal(map[string]any{"status": "ok", "retcode": 0, "data": data})
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestResolveEventMediaUsesGetFileAndPrefersRemoteURL(t *testing.T) {
	calls := map[string]int{}
	server := getFileServer(t, map[string]any{
		"file": "/Users/napcat/report.docx", "url": "https://cdn.example/report.docx",
		"file_name": "report.docx", "file_size": "58285",
	}, calls)
	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), AllowLocalMediaPaths: true}
	event := fileEvent("file-reference")
	bridge.resolveEventMedia(t.Context(), event)
	if calls["/get_file"] != 1 || calls["/get_private_file_url"] != 0 || calls["/get_group_file_url"] != 0 {
		t.Fatalf("calls=%v", calls)
	}
	if len(event.resolvedMedia) != 1 || event.resolvedMedia[0].URL != "https://cdn.example/report.docx" {
		t.Fatalf("media=%#v", event.resolvedMedia)
	}
}

func TestResolveEventMediaAcceptsLocalPathOnlyWhenAllowed(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		calls := map[string]int{}
		server := getFileServer(t, map[string]any{
			"file": "/Users/napcat/report.docx", "url": "/Users/napcat/report.docx",
			"file_name": "report.docx", "file_size": "58285",
		}, calls)
		bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), AllowLocalMediaPaths: allowed}
		event := fileEvent("file-reference")
		bridge.resolveEventMedia(t.Context(), event)
		if len(event.resolvedMedia) != 1 {
			t.Fatalf("allowed=%v media=%#v", allowed, event.resolvedMedia)
		}
		got := event.resolvedMedia[0].URL
		if allowed && got != "/Users/napcat/report.docx" {
			t.Fatalf("allowed=%v url=%q", allowed, got)
		}
		if !allowed && got != "" {
			t.Fatalf("local path leaked without opt-in: %q", got)
		}
		if allowed && event.resolvedMedia[0].Name != "report.docx" {
			t.Fatalf("file name not filled from get_file: %#v", event.resolvedMedia[0])
		}
	}
}

func TestResolveEventMediaFailureKeepsURLEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"failed","retcode":1404,"message":"real fileUUID not found!"}`))
	}))
	defer server.Close()
	bridge := &Bridge{APIURL: server.URL, HTTPClient: server.Client(), AllowLocalMediaPaths: true}
	event := fileEvent("file-reference")
	bridge.resolveEventMedia(t.Context(), event)
	if len(event.resolvedMedia) != 1 || event.resolvedMedia[0].URL != "" {
		t.Fatalf("media=%#v", event.resolvedMedia)
	}
}
