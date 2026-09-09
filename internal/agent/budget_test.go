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

	"github.com/Life-USTC/Bot/internal/store"
)

func TestAgentRunDeadlineLeavesConversationLeaseCommitMargin(t *testing.T) {
	if agentRunDeadline != 2*time.Minute {
		t.Fatalf("agent run deadline = %s, want 2m", agentRunDeadline)
	}
	if store.ConversationJobLease != 3*time.Minute {
		t.Fatalf("conversation job lease = %s, want 3m", store.ConversationJobLease)
	}
	if margin := store.ConversationJobLease - agentRunDeadline; margin < time.Minute {
		t.Fatalf("conversation job lease commit margin = %s, want at least 1m", margin)
	}
}

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

	// A single request wider than the provider window is a context error.
	if err := admitModelRequest(ctx, agentRequestTokenLimit); !errors.Is(err, errAgentContextBudget) {
		t.Fatalf("oversized single request error = %v", err)
	}
	// Full-window requests still accumulate, but against the run-wide cost
	// ceiling rather than against a single context window.
	fullWindow := agentRequestTokenLimit - kimiMaxCompletionTokens
	admitted := 0
	for admitted < 100 {
		err := admitModelRequest(ctx, fullWindow)
		if err == nil {
			admitted++
			continue
		}
		if !errors.Is(err, errAgentRunTokenBudget) {
			t.Fatalf("admission %d error = %v", admitted+1, err)
		}
		break
	}
	if admitted < 2 {
		t.Fatalf("run token budget must allow more than one full window, admitted=%d", admitted)
	}
	if got := metrics.snapshot().modelRequests; got != int64(admitted) {
		t.Fatalf("model requests = %d, want %d", got, admitted)
	}
}

// TestRunBudgetAllowsTheDocumentedToolLoop pins the bug that made the tool-call
// bound unreachable: at a realistic prompt size the run stopped on a context
// error around the sixth request, long before 12 tool calls.
func TestRunBudgetAllowsTheDocumentedToolLoop(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunBudget(context.Background(), budget)

	const realisticPromptTokens = 13_000
	for request := 1; request <= agentRunMaxToolCalls+1; request++ {
		if err := admitModelRequest(ctx, realisticPromptTokens); err != nil {
			t.Fatalf("request %d of the documented loop rejected: %v", request, err)
		}
		if request <= agentRunMaxToolCalls {
			if err := admitToolCall(ctx); err != nil {
				t.Fatalf("tool call %d rejected: %v", request, err)
			}
		}
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

func TestNormalizeAgentRunErrorDoesNotTreatUpstreamCancellationAsCallerCancellation(t *testing.T) {
	ctx := context.Background()
	budget := newRunBudget(time.Now(), newRunMetrics())
	err := normalizeAgentRunError(ctx, budget, context.Canceled)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("normalizeAgentRunError = %v, want visible upstream failure", err)
	}
	if reply := agentFailureReply(295, err); !strings.Contains(reply, "重新发送") || !strings.Contains(reply, "记录 #295") {
		t.Fatalf("agentFailureReply = %q", reply)
	}
}

func TestNormalizeAgentRunErrorPreservesEarlierCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	budget := newRunBudget(time.Now(), newRunMetrics())

	err := normalizeAgentRunError(ctx, budget, context.Canceled)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("normalizeAgentRunError = %v, want caller deadline", err)
	}
	if got := agentFailureClass(err); got != "request_timeout" {
		t.Fatalf("agentFailureClass = %q, want request_timeout", got)
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
		{err: errLLMUpstreamCanceled, class: "upstream_canceled"},
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
