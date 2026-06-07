package life

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	if !seen["/api/courses"] || !seen["/api/sections"] {
		t.Fatalf("seen paths = %#v", seen)
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
