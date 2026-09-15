package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type usageContextKey struct{}
type usagePersisterContextKey struct{}

type tokenUsage struct {
	PromptTokens     int64
	CachedTokens     int64
	CacheMissTokens  int64
	CompletionTokens int64
	TotalTokens      int64
	// CostNanoCNY is provider-reported when the response includes an explicit
	// cost. A zero value means spendingFor should use the configured estimate.
	CostNanoCNY   int64
	ModelRequests int64
	ToolCalls     int64
}

type usageAccumulator struct {
	mu    sync.Mutex
	total tokenUsage
}

func (a *usageAccumulator) add(usage tokenUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.total.PromptTokens += usage.PromptTokens
	a.total.CachedTokens += usage.CachedTokens
	a.total.CacheMissTokens += usage.CacheMissTokens
	a.total.CompletionTokens += usage.CompletionTokens
	a.total.TotalTokens += usage.TotalTokens
	a.total.CostNanoCNY += usage.CostNanoCNY
	a.total.ModelRequests += usage.ModelRequests
	a.total.ToolCalls += usage.ToolCalls
}

func (a *usageAccumulator) snapshot() tokenUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}

func withUsageAccumulator(ctx context.Context, accumulator *usageAccumulator) context.Context {
	return context.WithValue(ctx, usageContextKey{}, accumulator)
}

type usagePersister func(context.Context, tokenUsage) error

func withUsagePersister(ctx context.Context, persister usagePersister) context.Context {
	return context.WithValue(ctx, usagePersisterContextKey{}, persister)
}

func recordToolCall(ctx context.Context) {
	if accumulator, ok := ctx.Value(usageContextKey{}).(*usageAccumulator); ok {
		accumulator.add(tokenUsage{ToolCalls: 1})
	}
}

type usageCaptureTransport struct {
	base http.RoundTripper
}

func (t *usageCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	if isChatCompletionRequest(req) {
		recordRunStage(req.Context(), "model_response_headers", time.Since(started))
	}
	if err != nil || resp == nil || resp.Body == nil || !isChatCompletionRequest(req) || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	if isEventStream(resp) {
		resp.Body = newUsageStreamBody(req.Context(), resp.Body, started)
		return resp, nil
	}
	bodyStarted := time.Now()
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	recordRunStage(req.Context(), "model_response_body", time.Since(bodyStarted))
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

	captureProviderUsage(req.Context(), body)
	return resp, nil
}

func captureProviderUsage(ctx context.Context, body []byte) {
	var payload struct {
		Usage struct {
			PromptTokens          int64 `json:"prompt_tokens"`
			CompletionTokens      int64 `json:"completion_tokens"`
			TotalTokens           int64 `json:"total_tokens"`
			CachedTokens          int64 `json:"cached_tokens"`
			PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
			PromptTokenDetails    *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		CostNanoCNY int64 `json:"cost_nano_cny"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	cached := payload.Usage.PromptCacheHitTokens
	if cached == 0 {
		cached = payload.Usage.CachedTokens
	}
	if cached == 0 && payload.Usage.PromptTokenDetails != nil {
		cached = payload.Usage.PromptTokenDetails.CachedTokens
	}
	if cached > payload.Usage.PromptTokens {
		cached = payload.Usage.PromptTokens
	}
	miss := payload.Usage.PromptCacheMissTokens
	if miss == 0 {
		miss = payload.Usage.PromptTokens - cached
	}
	usage := tokenUsage{
		PromptTokens:     payload.Usage.PromptTokens,
		CachedTokens:     cached,
		CacheMissTokens:  miss,
		CompletionTokens: payload.Usage.CompletionTokens,
		TotalTokens:      payload.Usage.TotalTokens,
		CostNanoCNY:      payload.CostNanoCNY,
		ModelRequests:    1,
	}
	if accumulator, ok := ctx.Value(usageContextKey{}).(*usageAccumulator); ok {
		accumulator.add(usage)
	}
	if persister, ok := ctx.Value(usagePersisterContextKey{}).(usagePersister); ok && persister != nil {
		// Usage persistence is best effort. The provider response has already
		// succeeded, so a database error must not turn it into a retry that could
		// duplicate the external request; the durable attempt reservation remains.
		_ = persister(ctx, usage)
	}
	return
}

func spendingFor(provider, _ string, usage tokenUsage) store.AgentSpending {
	var cachedNanoPerToken, missedNanoPerToken, outputNanoPerToken int64
	switch provider {
	case "kimi":
		cachedNanoPerToken = kimiK3CachedNanoPerToken
		missedNanoPerToken = kimiK3MissedNanoPerToken
		outputNanoPerToken = kimiK3OutputNanoPerToken
	default:
		cachedNanoPerToken = deepseekCachedNanoPerToken
		missedNanoPerToken = deepseekMissedNanoPerToken
		outputNanoPerToken = deepseekOutputNanoPerToken
	}
	spending := store.AgentSpending{
		PromptTokens:     usage.PromptTokens,
		CachedTokens:     usage.CachedTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		ModelRequests:    usage.ModelRequests,
		ToolCalls:        usage.ToolCalls,
		CostNanoCNY: usage.CachedTokens*cachedNanoPerToken +
			usage.CacheMissTokens*missedNanoPerToken +
			usage.CompletionTokens*outputNanoPerToken,
		Currency: store.SpendingCurrencyCNY,
	}
	if usage.CostNanoCNY > 0 {
		spending.CostNanoCNY = usage.CostNanoCNY
	}
	return spending
}

const (
	kimiK3CachedNanoPerToken   int64 = 2_000
	kimiK3MissedNanoPerToken   int64 = 20_000
	kimiK3OutputNanoPerToken   int64 = 100_000
	deepseekCachedNanoPerToken int64 = 25
	deepseekMissedNanoPerToken int64 = 3_000
	deepseekOutputNanoPerToken int64 = 6_000
)
