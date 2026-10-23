package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
)

func TestTodoListRecoversAttemptTimeoutBeforeNumberedCompletion(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("todo request lost owner authorization")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/todos":
			if reads.Add(1) == 1 {
				<-r.Context().Done()
				return
			}
			_, _ = fmt.Fprint(w, `{"todos":[{"id":"first","title":"第一项","completed":false},{"id":"second","title":"第二项","completed":false}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspace/todos/second":
			writes.Add(1)
			_, _ = fmt.Fprint(w, `{"success":true,"todo":{"id":"second","title":"第二项","completed":true}}`)
		default:
			t.Errorf("unexpected todo request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)
	transport := server.Client()
	transport.Timeout = 100 * time.Millisecond
	handler.Life = life.NewClient(server.URL, transport)
	list, handled := handler.HandleOutcome(t.Context(), Input{Identity: ident, Text: "待办"})
	if !handled || list.Status != CapabilityOutcomeSuccess || !strings.Contains(list.Response.Text, "第二项") || list.Response.Image == nil || reads.Load() != 2 {
		t.Fatalf("list=%#v handled=%v reads=%d", list, handled, reads.Load())
	}
	completed, handled := handler.HandleOutcome(t.Context(), Input{Identity: ident, Text: "待办 完成 2"})
	if !handled || completed.Status != CapabilityOutcomeSuccess || completed.Response.Text != "已完成：第二项" || writes.Load() != 1 || reads.Load() != 3 {
		t.Fatalf("completion=%#v handled=%v reads=%d writes=%d", completed, handled, reads.Load(), writes.Load())
	}
}

func TestTodoCompletionTimeoutIsUnknownAndNeverReplayed(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/todos":
			reads.Add(1)
			_, _ = fmt.Fprint(w, `{"todos":[{"id":"first","title":"第一项","completed":false}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspace/todos/first":
			_, _ = io.Copy(io.Discard, r.Body)
			writes.Add(1)
			// The server may have applied the mutation before its response stalls.
			<-r.Context().Done()
		default:
			t.Errorf("unexpected todo request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)
	transport := server.Client()
	transport.Timeout = 100 * time.Millisecond
	handler.Life = life.NewClient(server.URL, transport)
	result, handled := handler.HandleOutcome(t.Context(), Input{Identity: ident, Text: "待办 完成 1"})
	if !handled || result.Status != CapabilityOutcomeUnknown || reads.Load() != 1 || writes.Load() != 1 {
		t.Fatalf("uncertain mutation=%#v handled=%v reads=%d writes=%d", result, handled, reads.Load(), writes.Load())
	}
}
