package commands

import (
	"context"
	"encoding/json"
	"github.com/Life-USTC/Bot/internal/life"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestTodoCompletionByExplicitIDDoesNotReadTheList(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit ID unexpectedly read the list")
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/workspace/todos/todo-201" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writes++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"completed":true`) {
			t.Fatalf("patch body = %s", body)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{
		Text:     "td done id:todo-201",
		Identity: ident,
	})
	if !ok || reply != "已完成。" || writes != 1 {
		t.Fatalf("reply = %q, ok = %v, writes = %d", reply, ok, writes)
	}
}

func TestTodoCompletionByExplicitIDReportsAPIFailureWithoutRetryingWrite(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit ID unexpectedly read the list")
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/workspace/todos/todo-201" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writes++
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{
		Text:     "td done id:todo-201",
		Identity: ident,
	})
	if !ok || !strings.Contains(reply, "待办完成失败") || writes != 1 {
		t.Fatalf("reply = %q, ok = %v, writes = %d", reply, ok, writes)
	}
}

func TestTodoUpdateUsesCanonicalServerTodoForTextAndData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit ID unexpectedly read the list")
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/workspace/todos/todo-201" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"todo":{"id":"todo-201","title":"服务端标题","content":"服务端内容","priority":"high","completed":false,"dueAt":null}}`))
	}))
	defer server.Close()

	ident := testIdentity()
	response, ok := testAuthedHandler(t, server, ident).HandleResponse(context.Background(), Input{
		Text:     "td update id:todo-201 title 请求标题",
		Identity: ident,
	})
	if !ok || response.Text != "已修改待办：服务端标题" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	data, ok := response.Data.(map[string]any)
	if !ok || data["operation"] != "update" {
		t.Fatalf("response data = %#v", response.Data)
	}
	item, ok := data["item"].(map[string]any)
	if !ok || item["title"] != "服务端标题" || item["content"] != "服务端内容" || item["priority"] != "high" {
		t.Fatalf("updated item = %#v", data["item"])
	}
}

func TestTodoDeleteByExplicitIDDoesNotReadTheList(t *testing.T) {
	var deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit ID unexpectedly read the list")
		}
		if r.Method != http.MethodDelete || r.URL.Path != "/api/workspace/todos/todo-201" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		deletes++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{
		Text:     "td delete id:todo-201",
		Identity: ident,
	})
	if !ok || reply != "已删除。" || deletes != 1 {
		t.Fatalf("reply = %q, ok = %v, deletes = %d", reply, ok, deletes)
	}
}

func TestTodoDeleteBatchStopsAfterUnknownFailure(t *testing.T) {
	var deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit IDs unexpectedly read the list")
		}
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		deletes++
		if deletes > 1 {
			t.Fatalf("delete continued after an uncertain result: %s", r.URL.Path)
		}
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{
		Text:     "td delete id:todo-201,id:todo-202",
		Identity: ident,
	})
	if !ok || !strings.Contains(reply, "待办删除失败") || deletes != 1 {
		t.Fatalf("reply = %q, ok = %v, deletes = %d", reply, ok, deletes)
	}
}

func TestDescribeTodoExplicitIDSkipsListPreflight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace/todos" {
			t.Fatalf("explicit ID unexpectedly read the list")
		}
		t.Fatalf("unexpected preflight request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	ident := testIdentity()
	description, err := testAuthedHandler(t, server, ident).DescribeInvocation(context.Background(), Input{Identity: ident}, CapabilityTodo, []string{"done", "id:todo-201"})
	if err != nil {
		t.Fatal(err)
	}
	if description.Invocation.Args[1] != "id:todo-201" || description.Receipt == nil || description.Receipt.Subject != "todo-201" {
		t.Fatalf("description = %#v", description)
	}
}

func TestTodoListMarksMaximumReadAsBounded(t *testing.T) {
	todos := make([]map[string]any, life.TodoListLimit)
	for i := range todos {
		todos[i] = map[string]any{"id": "todo-" + strconv.Itoa(i+1), "title": "待办"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "200" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"todos": todos})
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{Text: "td all", Identity: ident})
	if !ok || !strings.Contains(reply, "读取上限") || !strings.Contains(reply, "200") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestTodoListDoesNotClaimAnEmptyFilteredResultIsCompleteAtTheLimit(t *testing.T) {
	todos := make([]map[string]any, life.TodoListLimit)
	for i := range todos {
		todos[i] = map[string]any{"id": "todo-" + strconv.Itoa(i+1), "title": "已完成", "completed": true}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "200" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"todos": todos})
	}))
	defer server.Close()

	ident := testIdentity()
	reply, ok := testAuthedHandler(t, server, ident).Handle(context.Background(), Input{Text: "td", Identity: ident})
	if !ok || !strings.Contains(reply, "没有待办") || !strings.Contains(reply, "读取上限") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
