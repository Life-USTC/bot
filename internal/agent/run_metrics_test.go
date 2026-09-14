package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunMetricsAllowsLongLivedModelAndToolLoops(t *testing.T) {
	metrics := newRunMetrics()
	ctx := withRunMetrics(context.Background(), metrics)

	const requests = 1000
	for request := 0; request < requests; request++ {
		if err := admitModelRequest(ctx, 13_000); err != nil {
			t.Fatalf("model request %d rejected: %v", request+1, err)
		}
		if err := admitToolCall(ctx); err != nil {
			t.Fatalf("tool call %d rejected: %v", request+1, err)
		}
	}
	got := metrics.snapshot()
	if got.modelRequests != requests || got.toolCalls != requests {
		t.Fatalf("metrics = %#v, want %d model requests and tool calls", got, requests)
	}
}

func TestRunMetricsPropagatesCancellation(t *testing.T) {
	metrics := newRunMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withRunMetrics(ctx, metrics)
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
	err := normalizeAgentRunError(ctx, context.Canceled)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("normalizeAgentRunError = %v, want visible upstream failure", err)
	}
	if reply := agentFailureReply(295, err); !strings.Contains(reply, "重新发送") || !strings.Contains(reply, "记录 #295") {
		t.Fatalf("agentFailureReply = %q", reply)
	}
}

func TestNormalizeAgentRunErrorPreservesCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := normalizeAgentRunError(ctx, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("normalizeAgentRunError = %v, want caller cancellation", err)
	}
}

func TestAgentFailureClassesAreStable(t *testing.T) {
	for _, test := range []struct {
		err   error
		class string
	}{
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
	client := newAgentHTTPClient(&http.Client{Transport: base}, nil)
	metrics := newRunMetrics()
	ctx := withRunMetrics(context.Background(), metrics)
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
