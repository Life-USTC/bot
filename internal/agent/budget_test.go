package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBudgetStopsAnotherModelRequestAfterDeadline(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now().Add(-agentRunDeadline), metrics)
	ctx := withRunBudget(context.Background(), budget)

	if err := admitModelRequest(ctx, 1); !errors.Is(err, errAgentRunDeadline) {
		t.Fatalf("model admission error = %v", err)
	}
	if got := metrics.snapshot().modelRequests; got != 0 {
		t.Fatalf("model requests after deadline = %d", got)
	}
}

func TestRunBudgetCountsRetryReservationsCumulatively(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunBudget(context.Background(), budget)

	firstContext := agentRunTokenBudget - kimiMaxCompletionTokens
	if err := admitModelRequest(ctx, firstContext); err != nil {
		t.Fatalf("first model admission error = %v", err)
	}
	if err := admitModelRequest(ctx, 1); !errors.Is(err, errAgentContextBudget) {
		t.Fatalf("second model admission error = %v", err)
	}
	if got := metrics.snapshot().modelRequests; got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}
	if got := metrics.snapshot().contextTokens; got != firstContext {
		t.Fatalf("observed context tokens = %d, want %d", got, firstContext)
	}
}

func TestRunBudgetLimitsToolCalls(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunBudget(context.Background(), budget)

	for i := 0; i < agentRunMaxToolCalls; i++ {
		if err := admitToolCall(ctx); err != nil {
			t.Fatalf("tool admission %d = %v", i+1, err)
		}
	}
	if err := admitToolCall(ctx); !errors.Is(err, errAgentToolCallBudget) {
		t.Fatalf("tool admission over limit = %v", err)
	}
	if got := metrics.snapshot().toolCalls; got != int64(agentRunMaxToolCalls) {
		t.Fatalf("tool calls = %d, want %d", got, agentRunMaxToolCalls)
	}
}

func TestRunBudgetPropagatesCancellation(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withRunBudget(ctx, budget)
	cancel()

	if err := admitModelRequest(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled model admission error = %v", err)
	}
	if err := admitToolCall(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tool admission error = %v", err)
	}
}

func TestAgentFailureClassesAreStable(t *testing.T) {
	for _, test := range []struct {
		err   error
		class string
	}{
		{err: errAgentRunDeadline, class: "run_deadline"},
		{err: errAgentContextBudget, class: "context_budget"},
		{err: errAgentToolCallBudget, class: "tool_call_budget"},
		{err: errAgentNonProgress, class: "non_progress"},
		{err: errRepeatedToolCall, class: "repeated_tool_call"},
		{err: context.Canceled, class: "canceled"},
	} {
		if got := agentFailureClass(test.err); got != test.class {
			t.Errorf("agentFailureClass(%v) = %q, want %q", test.err, got, test.class)
		}
	}
}

func TestRetryTransportCountsEachAttemptAgainstRun(t *testing.T) {
	var attempts atomic.Int32
	base := scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		status := http.StatusInternalServerError
		body := `{}`
		if attempt == 2 {
			status = http.StatusOK
			body = `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	client := newAgentHTTPClient(&http.Client{Transport: base}, time.Second, nil)
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunMetrics(withRunBudget(context.Background(), budget), metrics)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://model.test/chat/completions", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if got := metrics.snapshot().modelRequests; got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
}

type scriptedRoundTripper func(*http.Request) (*http.Response, error)

func (f scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
