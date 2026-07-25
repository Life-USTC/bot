package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/Life-USTC/Bot/internal/store"
)

type usageContextKey struct{}

type tokenUsage struct {
	PromptTokens     int64
	CachedTokens     int64
	CacheMissTokens  int64
	CompletionTokens int64
	TotalTokens      int64
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
}

func (a *usageAccumulator) snapshot() tokenUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}

func withUsageAccumulator(ctx context.Context, accumulator *usageAccumulator) context.Context {
	return context.WithValue(ctx, usageContextKey{}, accumulator)
}

type usageCaptureTransport struct {
	base http.RoundTripper
}

func (t *usageCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || !isChatCompletionRequest(req) || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

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
	}
	if json.Unmarshal(body, &payload) != nil {
		return resp, nil
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
	if accumulator, ok := req.Context().Value(usageContextKey{}).(*usageAccumulator); ok {
		accumulator.add(tokenUsage{
			PromptTokens:     payload.Usage.PromptTokens,
			CachedTokens:     cached,
			CacheMissTokens:  miss,
			CompletionTokens: payload.Usage.CompletionTokens,
			TotalTokens:      payload.Usage.TotalTokens,
		})
	}
	return resp, nil
}

func spendingFor(provider, _ string, usage tokenUsage) store.AgentSpending {
	var cachedNanoPerToken, missedNanoPerToken, outputNanoPerToken int64
	switch provider {
	case "premium":
		cachedNanoPerToken = premiumCachedNanoPerToken
		missedNanoPerToken = premiumMissedNanoPerToken
		outputNanoPerToken = premiumOutputNanoPerToken
	default:
		cachedNanoPerToken = deepseekCachedNanoPerToken
		missedNanoPerToken = deepseekMissedNanoPerToken
		outputNanoPerToken = deepseekOutputNanoPerToken
	}
	return store.AgentSpending{
		PromptTokens:     usage.PromptTokens,
		CachedTokens:     usage.CachedTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CostNanoCNY: usage.CachedTokens*cachedNanoPerToken +
			usage.CacheMissTokens*missedNanoPerToken +
			usage.CompletionTokens*outputNanoPerToken,
		Currency: store.SpendingCurrencyCNY,
	}
}

const (
	premiumCachedNanoPerToken  int64 = 1_100
	premiumMissedNanoPerToken  int64 = 6_500
	premiumOutputNanoPerToken  int64 = 27_000
	deepseekCachedNanoPerToken int64 = 25
	deepseekMissedNanoPerToken int64 = 3_000
	deepseekOutputNanoPerToken int64 = 6_000
)
