package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestKimiMultimodalRunIsAvailableToEveryUserAndRecordsSpending(t *testing.T) {
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
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{
		Platform: "qqbot", UserID: "ordinary-user", ConversationType: "private", ConversationID: "ordinary-user",
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
	if requestBody["reasoning_effort"] != "low" || requestBody["max_completion_tokens"] != float64(kimiMaxCompletionTokens) {
		t.Fatalf("kimi limits = reasoning %#v max %#v", requestBody["reasoning_effort"], requestBody["max_completion_tokens"])
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
		total.TotalTokens != 12 || total.CostNanoCNY != 310_000 || total.ModelRequests != 1 {
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

func TestUsageCapturePersistsObservedUsageWithoutChangingAttemptReservation(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "usage", ConversationType: "private", ConversationID: "usage"}
	runID, err := db.RecordAgentRun(context.Background(), ident, store.AgentRun{JobID: 17, RawText: "usage"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		reserved, err := db.ReserveAgentModelAttempt(context.Background(), runID, 17, 5)
		if err != nil || !reserved {
			t.Fatalf("reserve %d: reserved=%v err=%v", i, reserved, err)
		}
	}
	persisted := func(ctx context.Context, usage tokenUsage) error {
		return db.RecordAgentUsage(ctx, runID, spendingFor("kimi", "kimi-k3", usage))
	}
	transport := &usageCaptureTransport{base: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		body := `{"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"cached_tokens":5},"cost_nano_cny":777}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	ctx := withUsagePersister(context.Background(), persisted)
	req := httptest.NewRequest(http.MethodPost, "https://compatible.example/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := db.FinishAgentRun(context.Background(), runID, store.AgentRunStatusInterrupted, "", nil, store.AgentSpending{ModelRequests: 1}); err != nil {
		t.Fatal(err)
	}
	spending, err := db.AgentJobSpending(context.Background(), 17)
	if err != nil {
		t.Fatal(err)
	}
	if spending.PromptTokens != 10 || spending.CachedTokens != 5 || spending.CompletionTokens != 2 || spending.TotalTokens != 12 || spending.CostNanoCNY != 777 || spending.ModelRequests != 3 {
		t.Fatalf("persisted usage = %#v", spending)
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

func TestLoadImageDataURLCompressesPayloadOverTenMiB(t *testing.T) {
	var source bytes.Buffer
	original := image.NewRGBA(image.Rect(0, 0, 2, 2))
	original.Set(0, 0, color.RGBA{R: 20, G: 40, B: 60, A: 255})
	if err := png.Encode(&source, original); err != nil {
		t.Fatal(err)
	}
	payload := append(source.Bytes(), make([]byte, maxImageBytes-source.Len()+1)...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client()}
	got, err := svc.loadImageDataURL(context.Background(), server.URL+"/large.png")
	if err != nil {
		t.Fatal(err)
	}
	header, encoded, found := strings.Cut(got, ",")
	if !found || header != "data:image/jpeg;base64" {
		t.Fatalf("data URL header = %q", header)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxImageBytes {
		t.Fatalf("compressed image size = %d", len(data))
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "jpeg" {
		t.Fatalf("compressed format = %q, err = %v", format, err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
