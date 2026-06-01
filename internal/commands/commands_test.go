package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/life"
)

func TestHandleCourseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "/life course calculus"})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "MATH1001 Calculus") {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "hello"})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
