package life

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTodosWithOptionsRequestsTheServerTodoLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got != "200" {
			t.Fatalf("limit = %q, want 200", got)
		}
		if r.URL.Query().Has("page") || r.URL.Query().Has("pageSize") {
			t.Error("legacy todo limit must not be combined with pagination")
		}
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, server.Client()).TodosWithOptions(context.Background(), "token", TodoListOptions{}); err != nil {
		t.Fatal(err)
	}
}
