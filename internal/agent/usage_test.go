package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestPremiumAdminMultimodalRunRecordsSpending(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"premium-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"我看到了。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"cached_tokens":5}
		}`)
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := New(context.Background(), Config{
		Enabled:        true,
		APIKey:         "default-key",
		BaseURL:        server.URL,
		Model:          "default-model",
		PremiumAPIKey:  "premium-key",
		PremiumBaseURL: server.URL,
		PremiumModel:   "premium-model",
		PremiumUserIDs: []string{" admin-id "},
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{
		Platform: "qqbot", UserID: "admin-id", ConversationType: "private", ConversationID: "admin-id",
	}
	response, ok := svc.HandleResponse(context.Background(), Input{
		Text: "看图",
		ImageURLs: []string{
			"data:image/png;base64,iVBORw0KGgo=",
		},
		Identity: ident,
	})
	if !ok || response.Text != "我看到了。" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if requestBody["model"] != "premium-model" {
		t.Fatalf("model = %#v", requestBody["model"])
	}
	messages, ok := requestBody["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", requestBody["messages"])
	}
	last, _ := messages[len(messages)-1].(map[string]any)
	content, ok := last["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("multimodal content = %#v", last["content"])
	}

	total, err := db.UserSpending(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if total.PromptTokens != 10 || total.CachedTokens != 5 || total.CompletionTokens != 2 ||
		total.TotalTokens != 12 || total.CostNanoCNY != 92_000 || total.ModelRequests != 1 {
		t.Fatalf("spending = %#v", total)
	}
}

func TestUsageCaptureReadsProviderCacheFields(t *testing.T) {
	accumulator := &usageAccumulator{}
	ctx := withUsageAccumulator(context.Background(), accumulator)
	transport := &usageCaptureTransport{base: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		body := `{"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	req := httptest.NewRequest(http.MethodPost, "https://compatible.example/v1/chat/completions", bytes.NewReader(nil)).WithContext(ctx)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got := accumulator.snapshot()
	if got.PromptTokens != 10 || got.CachedTokens != 4 || got.CacheMissTokens != 6 ||
		got.CompletionTokens != 3 || got.TotalTokens != 13 || got.ModelRequests != 1 {
		t.Fatalf("usage = %#v", got)
	}
}

func TestLoadImageDataURLDownloadsAndEncodesSupportedImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client()}
	got, err := svc.loadImageDataURL(context.Background(), server.URL+"/image")
	if err != nil {
		t.Fatal(err)
	}
	if got != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("data URL = %q", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
