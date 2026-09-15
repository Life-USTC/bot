package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// Every logical model request gets its own retry window. There is no
	// aggregate retry budget for a complete tool loop.
	llmHTTPMaxAttempts = llmRequestMaxAttempts
	llmRetryBaseDelay  = 200 * time.Millisecond
	llmRetryMaxDelay   = 5 * time.Second
)

var errLLMTransportExhausted = errors.New("llm transport retries exhausted")

func newAgentHTTPClient(base *http.Client, logger *log.Logger) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	}
	// The shared client may have a timeout for ordinary Bot HTTP calls. A
	// provider response can legitimately take longer than that, so the agent
	// client must not inherit a whole-response deadline. Request cancellation
	// still comes from the caller context, while transport-level connection and
	// handshake timeouts remain configured on the copied Transport.
	client.Timeout = 0
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	// Capture complete JSON responses inside the retry boundary. SSE bodies
	// remain incremental: once exposed to the SDK, a broken stream fails the
	// run instead of replaying a partially consumed model response.
	client.Transport = &llmRetryTransport{
		base: &usageCaptureTransport{
			base: transport,
		},
		logger: logger,
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

	started := time.Now()
	streaming := false
	defer func() {
		if !streaming {
			recordRunStage(req.Context(), "model_request", time.Since(started))
		}
	}()
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
		if errors.Is(err, context.Canceled) && attemptReq.Context().Err() == nil {
			// Some provider transports surface a canceled internal stream as
			// context.Canceled even though the caller's request is still live.
			// Keep it distinct from a real caller cancellation so it can be
			// retried and, if exhausted, reported to the user.
			err = errLLMUpstreamCanceled
		}
		if !shouldRetryLLMRequest(resp, err) || attempt == llmHTTPMaxAttempts {
			if attempt == llmHTTPMaxAttempts && isRetryableTransportError(err) {
				return resp, fmt.Errorf("%w: %w", errLLMTransportExhausted, err)
			}
			if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && isEventStream(resp) && resp.Body != nil {
				// Once stream bytes are exposed, read failures belong to this
				// attempt. Never replay a partially consumed model response.
				streaming = true
				resp.Body = &timedStreamBody{ReadCloser: resp.Body, ctx: req.Context(), started: started}
			}
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
		delay, err = capRetryDelay(attemptReq.Context(), delay)
		if err != nil {
			return nil, err
		}
		t.logRetry(req, attempt, delay, lastErr)
		if err := t.waitForRetry(attemptReq.Context(), delay); err != nil {
			if callerErr := callerContextError(attemptReq.Context()); callerErr != nil {
				return nil, callerErr
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
	if errors.Is(err, errLLMUpstreamCanceled) {
		return true
	}
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

func capRetryDelay(ctx context.Context, delay time.Duration) (time.Duration, error) {
	if err := callerContextError(ctx); err != nil {
		return 0, err
	}
	remaining := time.Duration(1<<63 - 1)
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			remaining = minDuration(remaining, time.Until(deadline))
		}
	}
	if remaining <= 0 {
		return 0, callerContextError(ctx)
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
