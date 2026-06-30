package life

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestSearchCourses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/courses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "math" {
			t.Fatalf("search = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Fatalf("limit = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	courses, err := client.SearchCourses(context.Background(), "math", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 1 || courses[0]["code"] != "MATH1001" {
		t.Fatalf("unexpected courses %#v", courses)
	}
}

func TestSearchTeachers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/teachers" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "张" {
			t.Fatalf("search = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "3" {
			t.Fatalf("limit = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"T001","namePrimary":"张三"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	teachers, err := client.SearchTeachers(context.Background(), " 张 ", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(teachers) != 1 || teachers[0]["namePrimary"] != "张三" {
		t.Fatalf("unexpected teachers %#v", teachers)
	}
}

func TestNewClientTrimsServerURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metadata" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(" "+server.URL+"/// ", server.Client())
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientSetsUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Fatalf("user-agent = %q", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSearchTrimsQuery(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		if got := r.URL.Query().Get("search"); got != "math" {
			t.Fatalf("%s search = %q", r.URL.Path, got)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.SearchCourses(context.Background(), " math ", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchSections(context.Background(), " math ", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchTeachers(context.Background(), " math ", 5); err != nil {
		t.Fatal(err)
	}
	if !seen["/api/courses"] || !seen["/api/sections"] || !seen["/api/teachers"] {
		t.Fatalf("seen paths = %#v", seen)
	}
}

func TestSearchQuery(t *testing.T) {
	values := searchQuery(" math ", 0)
	if values.Get("search") != "math" || values.Get("limit") != "5" {
		t.Fatalf("values = %s", values.Encode())
	}
}

func TestSchedulesUsesDataList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/schedules" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.URL.Query().Get("sectionId"); got != "101" {
			t.Fatalf("sectionId = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"startTime":"09:50","section":{"id":101}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	values := url.Values{"sectionId": []string{"101"}}
	schedules, err := client.Schedules(context.Background(), "token", values)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 || schedules[0]["startTime"] != "09:50" {
		t.Fatalf("schedules = %#v", schedules)
	}
}

func TestBusPreferencesUsesAuthEndpoints(t *testing.T) {
	var gotBody BusPreferences
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":1,"preferredDestinationCampusId":4,"showDepartedTrips":true}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/bus/preferences":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":2,"preferredDestinationCampusId":3,"showDepartedTrips":false}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	preferences, err := client.BusPreferences(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if preferences.PreferredOriginCampusID == nil || *preferences.PreferredOriginCampusID != 1 ||
		preferences.PreferredDestinationCampusID == nil || *preferences.PreferredDestinationCampusID != 4 ||
		!preferences.ShowDepartedTrips {
		t.Fatalf("preferences = %#v", preferences)
	}
	origin, destination := 2, 3
	preferences, err = client.SetBusPreferences(context.Background(), "token", BusPreferences{
		PreferredOriginCampusID:      &origin,
		PreferredDestinationCampusID: &destination,
		ShowDepartedTrips:            false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.PreferredOriginCampusID == nil || *gotBody.PreferredOriginCampusID != 2 ||
		gotBody.PreferredDestinationCampusID == nil || *gotBody.PreferredDestinationCampusID != 3 ||
		gotBody.ShowDepartedTrips {
		t.Fatalf("body = %#v", gotBody)
	}
	if preferences.PreferredOriginCampusID == nil || *preferences.PreferredOriginCampusID != 2 ||
		preferences.PreferredDestinationCampusID == nil || *preferences.PreferredDestinationCampusID != 3 ||
		preferences.ShowDepartedTrips {
		t.Fatalf("saved preferences = %#v", preferences)
	}
}

func TestScheduleQuery(t *testing.T) {
	values := ScheduleQuery(" 101 ", " 2026-06-07T00:00:00Z ", "\t2026-06-07T23:59:59Z\n")
	if values.Get("sectionId") != "101" ||
		values.Get("dateFrom") != "2026-06-07T00:00:00Z" ||
		values.Get("dateTo") != "2026-06-07T23:59:59Z" ||
		values.Get("limit") != "100" {
		t.Fatalf("values = %s", values.Encode())
	}
}

func TestTodosTrimsCompletedFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["completed"]; ok {
			t.Fatalf("completed query should be omitted: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.Todos(context.Background(), "token", "   "); err != nil {
		t.Fatal(err)
	}
}

func TestTodosWithOptionsSendsFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if values.Get("completed") != "true" || values.Get("priority") != "high" || values.Get("dueBefore") != "2026-06-10" || values.Get("dueAfter") != "2026-06-01" {
			t.Fatalf("query = %s", values.Encode())
		}
		_, _ = w.Write([]byte(`{"todos":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	_, err := client.TodosWithOptions(context.Background(), "token", TodoListOptions{
		Completed: " true ",
		Priority:  " high ",
		DueBefore: " 2026-06-10 ",
		DueAfter:  " 2026-06-01 ",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateTodoTrimsTitle(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"todo-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.CreateTodo(context.Background(), "token", " 写报告 "); err != nil {
		t.Fatal(err)
	}
	if gotBody["title"] != "写报告" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestCreateTodoRejectsBlankTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	todo, err := client.CreateTodo(context.Background(), "token", " \n ")
	if err == nil || !strings.Contains(err.Error(), "todo title") {
		t.Fatalf("todo = %#v, err = %v", todo, err)
	}
	if todo != nil {
		t.Fatalf("todo = %#v, want nil", todo)
	}
}

func TestCreateTodoWithOptionsSendsOptionalFields(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"todo-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	_, err := client.CreateTodoWithOptions(context.Background(), "token", TodoCreateOptions{
		Title:    " 写报告 ",
		Content:  " 读第一章 ",
		Priority: " high ",
		DueAt:    " 2026-06-10 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["title"] != "写报告" || gotBody["content"] != "读第一章" || gotBody["priority"] != "high" || gotBody["dueAt"] != "2026-06-10" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestCompleteTodoTrimsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos/todo-1" || r.Method != http.MethodPatch {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.CompleteTodo(context.Background(), "token", " todo-1 "); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTodoRejectsBlankID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.CompleteTodo(context.Background(), "token", " \t ")
	if err == nil || !strings.Contains(err.Error(), "todo id") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateTodoSendsChanges(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos/todo-1" || r.Method != http.MethodPatch {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	completed := false
	client := NewClient(server.URL, server.Client())
	err := client.UpdateTodo(context.Background(), "token", " todo-1 ", TodoUpdateOptions{
		Title:     " 写报告 ",
		Content:   " 读第一章 ",
		Priority:  " high ",
		DueAt:     " 2026-06-10 ",
		Completed: &completed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["title"] != "写报告" || gotBody["content"] != "读第一章" || gotBody["priority"] != "high" || gotBody["dueAt"] != "2026-06-10" || gotBody["completed"] != false {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestUpdateTodoRejectsEmptyChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.UpdateTodo(context.Background(), "token", "todo-1", TodoUpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "at least one change") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteTodoTrimsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos/todo-1" || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.DeleteTodo(context.Background(), "token", " todo-1 "); err != nil {
		t.Fatal(err)
	}
}

func TestSetHomeworkCompletionTrimsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/homeworks/homework-1/completion" || r.Method != http.MethodPut {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.SetHomeworkCompletion(context.Background(), "token", " homework-1 ", true); err != nil {
		t.Fatal(err)
	}
}

func TestSetHomeworkCompletionRejectsBlankID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.SetHomeworkCompletion(context.Background(), "token", "\n", true)
	if err == nil || !strings.Contains(err.Error(), "homework id") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetTodoCompletionsSendsBatch(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos/batch" || r.Method != http.MethodPatch {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.SetTodoCompletions(context.Background(), "token", []TodoCompletionItem{
		{TodoID: "todo-1", Completed: true},
		{TodoID: "todo-2", Completed: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v", gotBody["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["todoId"] != "todo-1" || first["completed"] != true {
		t.Fatalf("first item = %#v", first)
	}
	second, _ := items[1].(map[string]any)
	if second["todoId"] != "todo-2" || second["completed"] != false {
		t.Fatalf("second item = %#v", second)
	}
}

func TestSetTodoCompletionsReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.SetTodoCompletions(context.Background(), "token", []TodoCompletionItem{{TodoID: "todo-1", Completed: true}})
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestSetHomeworkCompletionsSendsBatch(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/homeworks/completions" || r.Method != http.MethodPut {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.SetHomeworkCompletions(context.Background(), "token", []HomeworkCompletionItem{
		{HomeworkID: "hw-1", Completed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v", gotBody["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["homeworkId"] != "hw-1" || first["completed"] != true {
		t.Fatalf("first item = %#v", first)
	}
}

func TestBulkSubscribeSectionsSendsCodes(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/calendar-subscriptions/import-codes" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"sections":[],"addedCount":0,"alreadySubscribedCount":0}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	out, err := client.BulkSubscribeSections(context.Background(), "token", []string{"MATH1001.01", "CS1001.02"})
	if err != nil {
		t.Fatal(err)
	}
	codes, ok := gotBody["codes"].([]any)
	if !ok || len(codes) != 2 || codes[0] != "MATH1001.01" || codes[1] != "CS1001.02" {
		t.Fatalf("codes = %#v", gotBody["codes"])
	}
	if out["addedCount"] != float64(0) {
		t.Fatalf("out = %#v", out)
	}
}

func TestBulkSubscribeSectionsReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	_, err := client.BulkSubscribeSections(context.Background(), "token", []string{"MATH1001.01"})
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestCompletionBody(t *testing.T) {
	for _, completed := range []bool{true, false} {
		body, err := completionBody(completed)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]bool
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got["completed"] != completed {
			t.Fatalf("completionBody(%v) = %s", completed, body)
		}
	}
}

func TestAuthHeaderTrimsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.Schedules(context.Background(), " token ", nil); err != nil {
		t.Fatal(err)
	}
}

func TestMatchSectionCodesTrimsSemesterID(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections/match-codes" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"sections":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.MatchSectionCodes(context.Background(), "token", []string{"MATH1001.01"}, "  2026-spring  "); err != nil {
		t.Fatal(err)
	}
	if gotBody["semesterId"] != "2026-spring" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestMatchSectionCodesTrimsCodes(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections/match-codes" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"sections":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if _, err := client.MatchSectionCodes(context.Background(), "token", []string{" MATH1001.01 ", " ", "CS1001.02"}, ""); err != nil {
		t.Fatal(err)
	}
	codes, ok := gotBody["codes"].([]any)
	if !ok || len(codes) != 2 || codes[0] != "MATH1001.01" || codes[1] != "CS1001.02" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestMatchSectionCodesRejectsBlankCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	matches, err := client.MatchSectionCodes(context.Background(), "token", []string{" ", "\t"}, "")
	if err == nil || !strings.Contains(err.Error(), "section code") {
		t.Fatalf("matches = %#v, err = %v", matches, err)
	}
	if matches != nil {
		t.Fatalf("matches = %#v, want nil", matches)
	}
}

func TestJSONBodyReturnsMarshalError(t *testing.T) {
	if _, err := jsonBody(map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("jsonBody returned nil error")
	}
}

func TestGetReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T", err)
	}
	if httpErr.StatusCode != http.StatusServiceUnavailable || httpErr.Method != http.MethodGet || httpErr.Path != "/api/metadata" {
		t.Fatalf("http error = %#v", httpErr)
	}
}

func TestGetReturnsHTTPErrorForErrorBodyReadFailure(t *testing.T) {
	client := NewClient("https://life.test", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(errReader{}),
		}, nil
	})})

	err := client.Health(context.Background())
	var httpErr HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T", err)
	}
	if httpErr.StatusCode != http.StatusServiceUnavailable || !strings.Contains(httpErr.Body, "read response body: read failed") {
		t.Fatalf("http error = %#v", httpErr)
	}
}

func TestGetTreatsWhitespaceBodyAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metadata" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(" \n\t "))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPErrorString(t *testing.T) {
	if got := (HTTPError{
		Method:     http.MethodGet,
		Path:       "/api/metadata",
		StatusCode: http.StatusServiceUnavailable,
		Body:       "not ready",
	}).Error(); got != "GET /api/metadata returned 503: not ready" {
		t.Fatalf("HTTPError with body = %q", got)
	}
	if got := (HTTPError{
		Method:     http.MethodGet,
		Path:       "/api/metadata",
		StatusCode: http.StatusServiceUnavailable,
	}).Error(); got != "GET /api/metadata returned 503" {
		t.Fatalf("HTTPError without body = %q", got)
	}
}

func TestTrimBodyTruncatesByRune(t *testing.T) {
	got := trimBody([]byte(strings.Repeat("错", 201)))
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed body is invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 200 {
		t.Fatalf("rune count = %d", utf8.RuneCountInString(got))
	}
	if got != strings.Repeat("错", 200) {
		t.Fatalf("trimmed body = %q", got)
	}
}

func TestMeFallsBackToOAuthUserinfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/me":
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		case "/api/auth/oauth2/userinfo":
			_, _ = w.Write([]byte(`{"sub":"user-1","preferred_username":"tiankai"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	me, err := client.Me(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if me["preferred_username"] != "tiankai" {
		t.Fatalf("me = %#v", me)
	}
}

func TestIsUnauthorized(t *testing.T) {
	if IsUnauthorized(nil) {
		t.Fatal("nil error reported unauthorized")
	}
	if !IsUnauthorized(errors.New(`GET /api/me returned 401: {"error":"Unauthorized"}`)) {
		t.Fatal("401 error was not recognized")
	}
	if !IsUnauthorized(errors.New("GET /api/me returned 401")) {
		t.Fatal("bodyless 401 error was not recognized")
	}
	if !IsUnauthorized(errors.New("GET /api/me RETURNED 401: Unauthorized")) {
		t.Fatal("case-insensitive 401 error was not recognized")
	}
	if !IsUnauthorized(HTTPError{StatusCode: http.StatusUnauthorized}) {
		t.Fatal("typed 401 error was not recognized")
	}
	if IsUnauthorized(errors.New("GET /api/me returned 500: nope")) {
		t.Fatal("non-401 error was recognized")
	}
	if IsUnauthorized(HTTPError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("typed non-401 error was recognized")
	}
}
