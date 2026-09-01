package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/textutil"
)

const (
	// A run-wide budget enforces this limit when the request context belongs to
	// an Agent run. The local loop is also bounded for callers without one.
	llmHTTPMaxAttempts = agentRunMaxModelAttempts
	llmRetryBaseDelay  = 200 * time.Millisecond
	llmRetryMaxDelay   = 5 * time.Second
)

func newAgentHTTPClient(base *http.Client, timeout time.Duration, logger *log.Logger) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	}
	client.Timeout = timeout
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &usageCaptureTransport{
		base: &llmRetryTransport{
			base:   transport,
			logger: logger,
		},
	}
	return &client
}

type llmRetryTransport struct {
	base   http.RoundTripper
	logger *log.Logger

	// These hooks keep retry behavior testable without sleeping or depending
	// on wall-clock time. Production callers leave them nil.
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
	now    func() time.Time
}

func (t *llmRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isChatCompletionRequest(req) {
		return t.base.RoundTrip(req)
	}

	budget := runBudgetFromContext(req.Context())
	runCtx, cancel := contextWithRunDeadline(req.Context(), budget)
	defer cancel()
	req = req.WithContext(runCtx)

	started := time.Now()
	defer func() { recordRunStage(req.Context(), "model_request", time.Since(started)) }()
	body, hasBody, err := reusableRequestBody(req)
	if err != nil {
		return nil, err
	}
	estimatedContextTokens := requestContextTokens(req, body, hasBody)
	var lastErr error
	for attempt := 1; attempt <= llmHTTPMaxAttempts; attempt++ {
		// Cloning can fail before a provider call is made. Admit only after it
		// succeeds so the atomic counter reflects physical attempts honestly.
		attemptReq, err := cloneRequestForRetry(req, body, hasBody)
		if err != nil {
			return nil, err
		}
		if attempt == 1 {
			err = admitModelRequest(attemptReq.Context(), estimatedContextTokens)
		} else {
			err = admitModelAttempt(attemptReq.Context())
		}
		if err != nil {
			return nil, err
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if !shouldRetryLLMRequest(resp, err) || attempt == llmHTTPMaxAttempts {
			return resp, err
		}

		lastErr = err
		if resp != nil {
			lastErr = errors.New(resp.Status)
			if resp.Body != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
				_ = resp.Body.Close()
			}
		}

		delay := t.retryDelay(attempt, resp)
		delay, err = capRetryDelay(attemptReq.Context(), budget, delay)
		if err != nil {
			return nil, err
		}
		t.logRetry(req, attempt, delay, lastErr)
		if err := t.waitForRetry(attemptReq.Context(), delay); err != nil {
			if budgetErr := budgetContextError(attemptReq.Context(), budget); budgetErr != nil {
				return nil, budgetErr
			}
			return nil, err
		}
	}
	return nil, lastErr
}

func requestContextTokens(req *http.Request, body []byte, hasBody bool) int64 {
	if !hasBody && req != nil && req.GetBody != nil {
		bodyReader, err := req.GetBody()
		if err == nil {
			body, _ = io.ReadAll(bodyReader)
			_ = bodyReader.Close()
		}
	}
	if len(body) == 0 {
		return 1
	}
	return int64(estimateTextTokens(string(body)))
}

func reusableRequestBody(req *http.Request) ([]byte, bool, error) {
	if req.Body == nil {
		return nil, false, nil
	}
	if req.GetBody != nil {
		return nil, false, req.Body.Close()
	}
	body, err := io.ReadAll(req.Body)
	if closeErr := req.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func cloneRequestForRetry(req *http.Request, body []byte, hasBody bool) (*http.Request, error) {
	clone := req.Clone(req.Context())
	switch {
	case req.GetBody != nil:
		bodyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = bodyReader
	case hasBody:
		clone.Body = io.NopCloser(bytes.NewReader(body))
	}
	return clone, nil
}

func shouldRetryLLMRequest(resp *http.Response, err error) bool {
	if err != nil {
		return isRetryableTransportError(err)
	}
	return resp != nil && isRetryableLLMStatus(resp.StatusCode)
}

func isRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// RoundTripper transport failures are normally wrapped in url.Error or
	// implement net.Error. EOFs cover a peer that closes a response mid-flight.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func isRetryableLLMStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isChatCompletionRequest(req *http.Request) bool {
	return req != nil &&
		req.Method == http.MethodPost &&
		req.URL != nil &&
		strings.HasSuffix(req.URL.Path, "/chat/completions")
}

// retryDelay is the bounded exponential component before jitter and any
// server-provided Retry-After value are applied.
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := llmRetryBaseDelay
	for step := 1; step < attempt; step++ {
		if delay >= llmRetryMaxDelay || delay > llmRetryMaxDelay/2 {
			return llmRetryMaxDelay
		}
		delay *= 2
	}
	return delay
}

func (t *llmRetryTransport) retryDelay(attempt int, resp *http.Response) time.Duration {
	if delay, ok := retryAfterDelay(resp, t.nowTime()); ok {
		return delay
	}
	return retryDelayWithJitter(attempt, t.jitter)
}

func retryDelayWithJitter(attempt int, jitter func(time.Duration) time.Duration) time.Duration {
	delay := retryDelay(attempt)
	if jitter == nil {
		jitter = defaultRetryJitter
	}
	extra := jitter(delay)
	if extra < 0 {
		extra = 0
	}
	if extra > llmRetryMaxDelay-delay {
		extra = llmRetryMaxDelay - delay
	}
	return delay + extra
}

func defaultRetryJitter(delay time.Duration) time.Duration {
	maxJitter := int64(delay / 2)
	if maxJitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(maxJitter + 1))
}

func retryAfterDelay(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	value := retryAfterHeader(resp.Header)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if !when.After(now) {
		return 0, true
	}
	return when.Sub(now), true
}

func retryAfterHeader(header http.Header) string {
	if len(header) == 0 {
		return ""
	}
	if value := header.Get("Retry-After"); value != "" {
		return strings.TrimSpace(value)
	}
	for key, values := range header {
		if strings.EqualFold(key, "Retry-After") && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func capRetryDelay(ctx context.Context, budget *runBudget, delay time.Duration) (time.Duration, error) {
	if err := budgetContextError(ctx, budget); err != nil {
		return 0, err
	}
	remaining := time.Duration(1<<63 - 1)
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			remaining = minDuration(remaining, time.Until(deadline))
		}
	}
	if budget != nil && !budget.deadline.IsZero() {
		remaining = minDuration(remaining, time.Until(budget.deadline))
	}
	if remaining <= 0 {
		return 0, budgetContextError(ctx, budget)
	}
	if delay > remaining {
		return remaining, nil
	}
	return delay, nil
}

func minDuration(left, right time.Duration) time.Duration {
	if right < left {
		return right
	}
	return left
}

func budgetContextError(ctx context.Context, budget *runBudget) error {
	if budget != nil {
		return budget.contextError(ctx)
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func (t *llmRetryTransport) waitForRetry(ctx context.Context, delay time.Duration) error {
	if t.wait != nil {
		return t.wait(ctx, delay)
	}
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *llmRetryTransport) nowTime() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *llmRetryTransport) logRetry(req *http.Request, attempt int, delay time.Duration, err error) {
	if t.logger == nil {
		return
	}
	t.logger.Printf("LLM request failed: method=%s path=%s attempt=%d/%d retry_in=%s error=%v",
		req.Method, req.URL.Path, attempt, llmHTTPMaxAttempts, delay, textutil.SafeLogError(err))
}
