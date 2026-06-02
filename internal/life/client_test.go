package life

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestGetReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected error")
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
