package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	llmHTTPMaxAttempts = 3
	llmRetryBaseDelay  = 200 * time.Millisecond
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
}

func (t *llmRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isChatCompletionRequest(req) {
		return t.base.RoundTrip(req)
	}
	body, hasBody, err := reusableRequestBody(req)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 1; attempt <= llmHTTPMaxAttempts; attempt++ {
		attemptReq, err := cloneRequestForRetry(req, body, hasBody)
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
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
		}
		delay := retryDelay(attempt)
		t.logRetry(req, attempt, delay, lastErr)
		timer := time.NewTimer(delay)
		select {
		case <-attemptReq.Context().Done():
			timer.Stop()
			return nil, attemptReq.Context().Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
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
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func isRetryableLLMStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func isChatCompletionRequest(req *http.Request) bool {
	return req != nil &&
		req.Method == http.MethodPost &&
		req.URL != nil &&
		strings.HasSuffix(req.URL.Path, "/chat/completions")
}

func retryDelay(attempt int) time.Duration {
	return llmRetryBaseDelay << (attempt - 1)
}

func (t *llmRetryTransport) logRetry(req *http.Request, attempt int, delay time.Duration, err error) {
	if t.logger == nil {
		return
	}
	t.logger.Printf("LLM request failed: method=%s path=%s attempt=%d/%d retry_in=%s error=%v",
		req.Method, req.URL.Path, attempt, llmHTTPMaxAttempts, delay, err)
}
