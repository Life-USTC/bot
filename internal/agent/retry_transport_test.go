package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTransportUsesExactlyRetryableStatuses(t *testing.T) {
	statuses := []struct {
		status       int
		retryable    bool
		wantAttempts int32
	}{
		{status: http.StatusRequestTimeout, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusTooManyRequests, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusInternalServerError, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusBadGateway, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusServiceUnavailable, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusGatewayTimeout, retryable: true, wantAttempts: llmHTTPMaxAttempts},
		{status: http.StatusBadRequest, retryable: false, wantAttempts: 1},
		{status: http.StatusUnauthorized, retryable: false, wantAttempts: 1},
		{status: http.StatusNotImplemented, retryable: false, wantAttempts: 1},
		{status: http.StatusHTTPVersionNotSupported, retryable: false, wantAttempts: 1},
	}
	for _, test := range statuses {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			var attempts atomic.Int32
			transport := &llmRetryTransport{
				base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
					attempts.Add(1)
					return retryTestResponse(req, test.status, nil), nil
				}),
				jitter: func(time.Duration) time.Duration { return 0 },
				wait:   func(context.Context, time.Duration) error { return nil },
			}
			resp, err := transport.RoundTrip(retryTestRequest(context.Background()))
			if err != nil {
				t.Fatalf("RoundTrip error = %v", err)
			}
			if resp == nil || resp.StatusCode != test.status {
				t.Fatalf("response = %#v", resp)
			}
			_ = resp.Body.Close()
			if got := attempts.Load(); got != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, test.wantAttempts)
			}
			if got := shouldRetryLLMRequest(resp, nil); got != test.retryable {
				t.Fatalf("shouldRetryLLMRequest(%d) = %v, want %v", test.status, got, test.retryable)
			}
		})
	}
}

func TestRetryTransportRetriesOnlyRetryableTransportErrors(t *testing.T) {
	for _, test := range []struct {
		name         string
		transportErr error
		wantAttempts int32
	}{
		{name: "unexpected EOF", transportErr: io.ErrUnexpectedEOF, wantAttempts: llmHTTPMaxAttempts},
		{name: "url error", transportErr: &url.Error{Op: "POST", URL: "http://model.test", Err: io.ErrUnexpectedEOF}, wantAttempts: llmHTTPMaxAttempts},
		{name: "permanent error", transportErr: errors.New("invalid request"), wantAttempts: 1},
		{name: "canceled", transportErr: context.Canceled, wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			transport := &llmRetryTransport{
				base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
					attempts.Add(1)
					return nil, test.transportErr
				}),
				jitter: func(time.Duration) time.Duration { return 0 },
				wait:   func(context.Context, time.Duration) error { return nil },
			}
			_, err := transport.RoundTrip(retryTestRequest(context.Background()))
			if !errors.Is(err, test.transportErr) {
				t.Fatalf("RoundTrip error = %v, want %v", err, test.transportErr)
			}
			if got := attempts.Load(); got != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, test.wantAttempts)
			}
		})
	}
}

func TestRetryTransportHonorsRetryAfterSecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "seconds", header: "2", want: 2 * time.Second},
		{name: "http-date", header: now.Add(3 * time.Second).Format(http.TimeFormat), want: 3 * time.Second},
		{name: "past-date", header: now.Add(-time.Second).Format(http.TimeFormat), want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp := retryTestResponse(nil, http.StatusTooManyRequests, http.Header{"Retry-After": []string{test.header}})
			if got, ok := retryAfterDelay(resp, now); !ok || got != test.want {
				t.Fatalf("retryAfterDelay = (%s, %v), want (%s, true)", got, ok, test.want)
			}
		})
	}

	var delays []time.Duration
	transport := &llmRetryTransport{
		base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
			if len(delays) == 0 {
				return retryTestResponse(req, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"2"}}), nil
			}
			return retryTestResponse(req, http.StatusOK, nil), nil
		}),
		now:    func() time.Time { return now },
		jitter: func(time.Duration) time.Duration { return 0 },
		wait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	resp, err := transport.RoundTrip(retryTestRequest(context.Background()))
	if err != nil {
		t.Fatalf("RoundTrip error = %v", err)
	}
	_ = resp.Body.Close()
	if len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("retry delays = %v, want [2s]", delays)
	}
}

func TestRetryTransportCapsBackoffAndRemainingDeadline(t *testing.T) {
	if got := retryDelayWithJitter(100, func(time.Duration) time.Duration { return 24 * time.Hour }); got != llmRetryMaxDelay {
		t.Fatalf("bounded retry delay = %s, want %s", got, llmRetryMaxDelay)
	}

	metrics := newRunMetrics()
	budget := newRunBudget(time.Now().Add(-(agentRunDeadline - 25*time.Millisecond)), metrics)
	ctx := withRunMetrics(withRunBudget(context.Background(), budget), metrics)
	var attempts atomic.Int32
	transport := &llmRetryTransport{
		base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return retryTestResponse(req, http.StatusServiceUnavailable, nil), nil
		}),
		jitter: func(time.Duration) time.Duration { return 0 },
	}
	started := time.Now()
	resp, err := transport.RoundTrip(retryTestRequest(ctx))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errAgentRunDeadline) {
		t.Fatalf("RoundTrip error = %v, want run deadline", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 before deadline", got)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline wait took %s", elapsed)
	}
}

func TestRetryTransportStopsBackoffOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	transport := &llmRetryTransport{
		base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
			attempts.Add(1)
			cancel()
			return retryTestResponse(req, http.StatusBadGateway, nil), nil
		}),
		jitter: func(time.Duration) time.Duration { return 0 },
	}
	resp, err := transport.RoundTrip(retryTestRequest(ctx))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip error = %v, want cancellation", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestRunBudgetLimitsPhysicalAttemptsAcrossRequests(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunMetrics(withRunBudget(context.Background(), budget), metrics)
	var attempts atomic.Int32
	transport := &llmRetryTransport{
		base: scriptedRoundTripper(func(req *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return retryTestResponse(req, http.StatusInternalServerError, nil), nil
		}),
		jitter: func(time.Duration) time.Duration { return 0 },
		wait:   func(context.Context, time.Duration) error { return nil },
	}

	logicalRequests := agentRunMaxModelAttempts / llmHTTPMaxAttempts
	for request := 0; request < logicalRequests; request++ {
		resp, err := transport.RoundTrip(retryTestRequest(ctx))
		if err != nil {
			t.Fatalf("RoundTrip %d error = %v", request+1, err)
		}
		_ = resp.Body.Close()
	}
	if _, err := transport.RoundTrip(retryTestRequest(ctx)); !errors.Is(err, errAgentModelAttemptBudget) {
		t.Fatalf("request beyond aggregate limit error = %v, want attempt budget", err)
	}
	if got := attempts.Load(); got != int32(agentRunMaxModelAttempts) {
		t.Fatalf("physical attempts = %d, want %d", got, agentRunMaxModelAttempts)
	}
	if got := metrics.snapshot().modelRequests; got != int64(agentRunMaxModelAttempts) {
		t.Fatalf("recorded model requests = %d, want %d", got, agentRunMaxModelAttempts)
	}
}

func TestRunBudgetAttemptAdmissionRace(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunMetrics(withRunBudget(context.Background(), budget), metrics)
	const workers = 128
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := admitModelAttempt(ctx); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, errAgentModelAttemptBudget) {
				t.Errorf("admitModelAttempt error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != int32(agentRunMaxModelAttempts) {
		t.Fatalf("successful admissions = %d, want %d", got, agentRunMaxModelAttempts)
	}
	if got := metrics.snapshot().modelRequests; got != int64(agentRunMaxModelAttempts) {
		t.Fatalf("recorded model requests = %d, want %d", got, agentRunMaxModelAttempts)
	}
}

func TestRunBudgetRetriesDoNotReserveTokensAgain(t *testing.T) {
	metrics := newRunMetrics()
	budget := newRunBudget(time.Now(), metrics)
	ctx := withRunMetrics(withRunBudget(context.Background(), budget), metrics)
	firstContext := agentRunTokenBudget - kimiMaxCompletionTokens
	if err := admitModelRequest(ctx, firstContext); err != nil {
		t.Fatalf("first model admission error = %v", err)
	}
	for attempt := 2; attempt <= agentRunMaxModelAttempts; attempt++ {
		if err := admitModelAttempt(ctx); err != nil {
			t.Fatalf("retry admission %d = %v", attempt, err)
		}
	}
	if got := metrics.snapshot().modelRequests; got != int64(agentRunMaxModelAttempts) {
		t.Fatalf("recorded model requests = %d, want %d", got, agentRunMaxModelAttempts)
	}
}

func retryTestRequest(ctx context.Context) *http.Request {
	return mustRetryTestRequest(ctx, strings.NewReader(`{"messages":[]}`))
}

func mustRetryTestRequest(ctx context.Context, body io.Reader) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://model.test/v1/chat/completions", body)
	if err != nil {
		panic(err)
	}
	return req
}

func retryTestResponse(req *http.Request, status int, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}
}
