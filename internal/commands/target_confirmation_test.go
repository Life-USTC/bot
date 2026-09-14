package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMutationPreflightFreezesTargetsBeforeListChanges(t *testing.T) {
	items := []map[string]any{{"id": "a", "title": "第一项"}, {"id": "b", "title": "第二项"}, {"id": "c", "title": "第三项"}}
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/todos":
			_ = json.NewEncoder(w).Encode(map[string]any{"todos": items})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/workspace/todos/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/workspace/todos/")
			deleted = append(deleted, id)
			remaining := make([]map[string]any, 0, len(items))
			for _, item := range items {
				if item["id"] != id {
					remaining = append(remaining, item)
				}
			}
			items = remaining
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)
	invocation := ParseCommand("待办 删除 1,2").Invocation
	var descriptions []CapabilityInvocationDescription
	for _, expanded := range ExpandMutationInvocations(invocation) {
		description, err := handler.DescribeInvocation(t.Context(), Input{Identity: ident}, expanded.ID(), expanded.Args)
		if err != nil {
			t.Fatal(err)
		}
		descriptions = append(descriptions, description)
	}
	if len(descriptions) != 2 || descriptions[0].Invocation.Args[1] != "id:a" || descriptions[1].Invocation.Args[1] != "id:b" || !strings.Contains(descriptions[1].Receipt.Subject, "第二项") {
		t.Fatalf("descriptions=%#v", descriptions)
	}
	for _, description := range descriptions {
		outcome, err := handler.ExecuteCapability(t.Context(), Input{Identity: ident}, description.Invocation.ID(), description.Invocation.Args)
		if err != nil || outcome.Status != CapabilityOutcomeSuccess {
			t.Fatalf("outcome=%#v err=%v", outcome, err)
		}
	}
	if !reflect.DeepEqual(deleted, []string{"a", "b"}) {
		t.Fatalf("deleted=%v", deleted)
	}
}

func TestExactTargetIDDoesNotFallBackToAnotherItem(t *testing.T) {
	items := []map[string]any{{"id": "a", "title": "报告甲"}, {"id": "b", "title": "报告乙"}}
	if _, ok := resolveByTarget(items, "报告"); ok {
		t.Fatal("ambiguous title selected a target")
	}
	if _, ok := resolveByTarget(items, "id:missing"); ok {
		t.Fatal("missing exact ID selected a target")
	}
	numeric := []map[string]any{{"id": "2", "title": "实际ID"}, {"id": "other", "title": "第二条"}}
	got, ok := resolveByTarget(numeric, "id:2")
	if !ok || got["id"] != "2" {
		t.Fatalf("explicit numeric ID=%#v", got)
	}
}
