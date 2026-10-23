package life

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTodosRetriesAnAttemptTimeoutWhileCallerIsActive(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					if phase == "body" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, `{"todos":[`)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					return
				}
				_, _ = fmt.Fprint(w, `{"todos":[{"id":"todo-1","title":"合成待办"}]}`)
			}))
			t.Cleanup(server.Close)
			transport := server.Client()
			transport.Timeout = 100 * time.Millisecond
			client := NewClient(server.URL, transport)
			rows, err := client.Todos(t.Context(), "owner", "false")
			if err != nil || requests.Load() != 2 || len(rows) != 1 || rows[0]["id"] != "todo-1" {
				t.Fatalf("single-attempt %s timeout aborted active caller: requests=%d rows=%#v err=%v", phase, requests.Load(), rows, err)
			}
		})
	}
}

func TestTodosDoesNotRetryAfterCallerDeadline(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := NewClient(server.URL, server.Client()).Todos(ctx, "owner", "false")
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("expired caller retried: requests=%d err=%v", requests.Load(), err)
	}
}

func TestTodosDoesNotRetryCancelledCaller(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `{"todos":[]}`)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewClient(server.URL, server.Client()).Todos(ctx, "owner", "false")
	if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
		t.Fatalf("cancelled caller sent a request: requests=%d err=%v", requests.Load(), err)
	}
}

func TestTodosDoesNotRetryAuthorizationOrInvalidResponse(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"unauthorized"}`},
		{"forbidden", http.StatusForbidden, `{"error":"forbidden"}`},
		{"malformed-json", http.StatusOK, `{"todos":[`},
		{"wrong-field-type", http.StatusOK, `{"todos":"invalid"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)
			_, err := NewClient(server.URL, server.Client()).Todos(t.Context(), "owner", "false")
			if err == nil || requests.Load() != 1 {
				t.Fatalf("permanent response retried: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestTodosAttemptTimeoutRetainsReadAttemptLimit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	transport := server.Client()
	transport.Timeout = 100 * time.Millisecond
	_, err := NewClient(server.URL, transport).Todos(t.Context(), "owner", "false")
	if err == nil || requests.Load() != 3 {
		t.Fatalf("timeout attempts=%d err=%v", requests.Load(), err)
	}
}
