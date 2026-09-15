package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestStreamingTransportDeliversIncrementallyAndCapturesUsageOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		first := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\n"
		usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12,\"cached_tokens\":6}}\n\n"
		last := usage + usage + "data: [DONE]\n\n"
		go func() {
			time.Sleep(time.Second)
			_, _ = io.WriteString(writer, first)
			time.Sleep(2 * time.Second)
			_, _ = io.WriteString(writer, last)
			_ = writer.Close()
		}()
		metrics := newRunMetrics()
		accumulator := &usageAccumulator{}
		ctx := withUsageAccumulator(withRunMetrics(context.Background(), metrics), accumulator)
		client := newAgentHTTPClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: reader, Request: req}, nil
		})}, nil)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://model.test/chat/completions", strings.NewReader(`{"stream":true}`))
		started := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(started) != 0 {
			t.Fatal("response headers blocked on stream body")
		}
		buf := make([]byte, len(first))
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != first || time.Since(started) != time.Second {
			t.Fatalf("first frame delayed or changed: %q", buf)
		}
		if accumulator.snapshot().ModelRequests != 0 {
			t.Fatal("usage fabricated before final event")
		}
		rest, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(rest) != last {
			t.Fatalf("wire bytes changed: %q", rest)
		}
		_ = resp.Body.Close()
		_ = resp.Body.Close()
		got := accumulator.snapshot()
		if got.ModelRequests != 1 || got.CachedTokens != 6 || got.TotalTokens != 12 {
			t.Fatalf("usage = %#v", got)
		}
		stages := metrics.snapshot().stageMilliseconds
		if stages["model_first_text"] != 1000 || stages["model_response_body"] != 3000 || stages["model_request"] != 3000 {
			t.Fatalf("stages = %#v", stages)
		}
	})
}

func TestStreamingTransportDoesNotReplayTruncatedResponse(t *testing.T) {
	attempts := 0
	client := newAgentHTTPClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")), Request: req}, nil
	})}, nil)
	resp, err := client.Post("https://model.test/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("partially consumed response replayed %d times", attempts)
	}
}

func TestStreamingBodyCloseUnblocksReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		body := newUsageStreamBody(context.Background(), reader, time.Now())
		done := make(chan error, 1)
		go func() { _, err := io.ReadAll(body); done <- err }()
		synctest.Wait()
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("read after close = %v", err)
		}
	})
}
