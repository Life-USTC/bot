package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

// Neither classification nor logging may inspect Error(), even through wraps.
type todoDiagnosticOpaqueError struct{ cause error }

func (todoDiagnosticOpaqueError) Error() string   { panic("diagnostic inspected Error()") }
func (e todoDiagnosticOpaqueError) Unwrap() error { return e.cause }

func TestTodoReadFailureMetadataUsesOnlyFiniteTypedCategories(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want todoReadDiagnostic
	}{
		{"canceled", context.Canceled, todoReadDiagnostic{"read_unspecified", "failed", "canceled"}},
		{"deadline", context.DeadlineExceeded, todoReadDiagnostic{"read_unspecified", "failed", "deadline"}},
		{"no credential", auth.ErrNotLoggedIn, todoReadDiagnostic{"auth_gate", "auth_required", "not_logged_in"}},
		{"reauthorization", auth.ErrReauthorizationRequired, todoReadDiagnostic{"auth_gate", "auth_required", "reauthorization_required"}},
		{"unexpected EOF", io.ErrUnexpectedEOF, todoReadDiagnostic{"read_unspecified", "failed", "unexpected_eof"}},
		{"private URL", &url.Error{Op: "private-operation", URL: "https://private.example/private-file?token=private-token", Err: todoDiagnosticOpaqueError{}}, todoReadDiagnostic{"read_unspecified", "failed", "network_error"}},
		{"DNS timeout", &net.DNSError{Err: "private-error", Name: "private-host", IsTimeout: true}, todoReadDiagnostic{"read_unspecified", "failed", "network_timeout"}},
		{"network error", &net.OpError{Op: "private-operation", Net: "private-network", Err: todoDiagnosticOpaqueError{}}, todoReadDiagnostic{"read_unspecified", "failed", "network_error"}},
		{"syntax", &json.SyntaxError{}, todoReadDiagnostic{"json_processing", "failed", "json_syntax"}},
		{"private JSON fields", &json.UnmarshalTypeError{Value: "private-title", Type: reflect.TypeFor[int](), Struct: "private-struct", Field: "private-field"}, todoReadDiagnostic{"json_processing", "failed", "json_type"}},
		{"marshal", &json.MarshalerError{Type: reflect.TypeFor[int](), Err: &json.SyntaxError{}}, todoReadDiagnostic{"json_processing", "failed", "json_marshal"}},
		{"message is not evidence", errors.New("HTTP 401 timeout decode todos: private-token"), todoReadDiagnostic{"unknown", "failed", "unknown"}},
		{"opaque", todoDiagnosticOpaqueError{}, todoReadDiagnostic{"unknown", "failed", "unknown"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := todoDiagnosticOpaqueError{cause: tt.err}
			if got := classifyTodoReadFailure(err); got != tt.want {
				t.Fatalf("metadata = %#v, want %#v", got, tt.want)
			}
			var logs bytes.Buffer
			Handler{Logger: log.New(&logs, "", 0)}.logTodoReadFailure(err)
			want := "todo read failed: operation=todo_list phase=" + tt.want.phase + " outcome=" + tt.want.outcome + " reason=" + tt.want.reason + "\n"
			if logs.String() != want {
				t.Fatalf("metadata did not match the complete finite record: %q", logs.String())
			}
		})
	}
	for _, tt := range []struct {
		status  int
		reason  string
		outcome string
	}{
		{401, "http_401", "auth_required"}, {403, "http_403", "failed"},
		{408, "http_408", "failed"}, {429, "http_429", "failed"},
		{400, "http_4xx", "failed"}, {503, "http_5xx", "failed"},
		{302, "http_other", "failed"}, {999, "http_other", "failed"},
	} {
		err := todoDiagnosticOpaqueError{cause: life.HTTPError{Method: "private-method", Path: "/private-id", Body: "private-body-token", StatusCode: tt.status}}
		want := todoReadDiagnostic{"http_response", tt.outcome, tt.reason}
		if got := classifyTodoReadFailure(err); got != want {
			t.Fatalf("status category %d = %#v, want %#v", tt.status, got, want)
		}
		var logs bytes.Buffer
		Handler{Logger: log.New(&logs, "", 0)}.logTodoReadFailure(err)
		if strings.Contains(logs.String(), "private") {
			t.Fatal("HTTP private fields entered metadata")
		}
	}
}

func TestTodoReadFailureObservesFinalResultWithoutAddedCalls(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		body     string
		requests int32
		metadata string
	}{
		{"exhausted transient", 503, "private-response-body", 3, "phase=http_response outcome=failed reason=http_5xx"},
		{"unauthorized", 401, "private-response-body", 1, "phase=http_response outcome=auth_required reason=http_401"},
		{"forbidden", 403, "private-response-body", 1, "phase=http_response outcome=failed reason=http_403"},
		{"invalid JSON", 200, `{private-token`, 1, "phase=json_processing outcome=failed reason=json_syntax"},
		{"invalid shape", 200, `{"todos":"private-title"}`, 1, "phase=json_processing outcome=failed reason=json_type"},
		{"untyped null model", 200, `null`, 1, "phase=unknown outcome=failed reason=unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/workspace/todos" || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Error("read request or owner authorization changed")
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			var logs bytes.Buffer
			h := Handler{Life: life.NewClient(server.URL, server.Client()), Logger: log.New(&logs, "", 0)}
			items, err := h.todos(t.Context(), store.Identity{}, "private-token", life.TodoListOptions{})
			if err == nil || items != nil || requests.Load() != tt.requests {
				t.Fatalf("read boundary changed: failed=%t items=%d requests=%d", err != nil, len(items), requests.Load())
			}
			if tt.status >= 400 {
				original, ok := err.(life.HTTPError)
				if !ok || original.StatusCode != tt.status || original.Body != tt.body {
					t.Fatal("original HTTP error was replaced or changed")
				}
			}
			if logs.String() != "todo read failed: operation=todo_list "+tt.metadata+"\n" {
				t.Fatalf("expected exactly one final finite record, got %q", logs.String())
			}
		})
	}
}

func TestTodoReadRecoveryDoesNotLogAnIntermediateFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"todos":[{"id":"private-id","title":"private-title","completed":false}]}`)
	}))
	t.Cleanup(server.Close)
	var logs bytes.Buffer
	h := Handler{Life: life.NewClient(server.URL, server.Client()), Logger: log.New(&logs, "", 0)}
	for _, wantRequests := range []int32{2, 3} {
		items, err := h.todos(t.Context(), store.Identity{}, "private-token", life.TodoListOptions{})
		if err != nil || len(items) != 1 || items[0]["title"] != "private-title" || requests.Load() != wantRequests || logs.Len() != 0 {
			t.Fatal("recovered or ordinary success changed, or emitted failure metadata")
		}
	}
}

func TestTodoReadCancellationKeepsOriginalErrorAndDoesNotStartWork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)
	var logs bytes.Buffer
	h := Handler{Life: life.NewClient(server.URL, server.Client()), Logger: log.New(&logs, "", 0)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := h.todos(ctx, store.Identity{}, "private-token", life.TodoListOptions{})
	if err != ctx.Err() || err != context.Canceled || requests.Load() != 0 {
		t.Fatal("caller cancellation sentinel or zero-request boundary changed")
	}
	if logs.String() != "todo read failed: operation=todo_list phase=read_unspecified outcome=failed reason=canceled\n" {
		t.Fatalf("cancellation metadata = %q", logs.String())
	}
}

func TestTodoReadAuthRefreshSucceedsWithoutFailureMetadata(t *testing.T) {
	var reads, refreshes atomic.Int32
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case handleOAuthRefreshMetadata(w, r, serverURL):
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			refreshes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/todos":
			reads.Add(1)
			if r.Header.Get("Authorization") == "Bearer access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer refreshed" {
				t.Error("refresh changed owner authorization")
			}
			_, _ = io.WriteString(w, `{"todos":[]}`)
		default:
			t.Error("diagnostic started an unexpected request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	t.Cleanup(server.Close)
	var logs bytes.Buffer
	h := testAuthedHandlerWithRefresh(t, server, testIdentity())
	h.Logger = log.New(&logs, "", 0)
	_, err := h.todos(t.Context(), testIdentity(), "access", life.TodoListOptions{})
	if err != nil || reads.Load() != 2 || refreshes.Load() != 1 || logs.Len() != 0 {
		t.Fatal("refresh/recovery counts changed, or intermediate auth failure was logged")
	}
}

func TestTodoWriteFailureDoesNotBecomeReadMetadataOrReplay(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/workspace/todos/private-id" {
			writes.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		reads.Add(1)
		t.Error("explicit write unexpectedly started other work")
	}))
	t.Cleanup(server.Close)
	var logs bytes.Buffer
	h := testAuthedHandler(t, server, testIdentity())
	h.Logger = log.New(&logs, "", 0)
	outcome, handled := h.HandleOutcome(t.Context(), Input{Identity: testIdentity(), Text: "td done id:private-id"})
	if !handled || outcome.Status != CapabilityOutcomeUnknown || writes.Load() != 1 || reads.Load() != 0 || logs.Len() != 0 {
		t.Fatal("uncertain write behavior or read-only diagnostic boundary changed")
	}
	if !strings.Contains(outcome.Response.Text, "系统不会自动重试") {
		t.Fatal("existing uncertain-write reply changed")
	}
}
