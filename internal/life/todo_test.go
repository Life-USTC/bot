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
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, server.Client()).TodosWithOptions(context.Background(), "token", TodoListOptions{}); err != nil {
		t.Fatal(err)
	}
}
