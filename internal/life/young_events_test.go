package life

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListYoungEventsUsesPublicPaginationAndSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/catalog/young-events" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("page") != "2" || query.Get("pageSize") != "10" || query.Get("search") != "志愿" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("public request unexpectedly sent authorization %q", got)
		}
		_, _ = w.Write([]byte(`{
			"data":[{"youngId":"event-2","name":"志愿服务","location":"东区图书馆","startAt":"2026-09-14T12:00:00Z","endAt":null,"applyStartAt":null,"applyEndAt":"2026-09-13T16:00:00+08:00","isActive":true}],
			"pagination":{"page":2,"pageSize":10,"total":11,"totalPages":2}
		}`))
	}))
	defer server.Close()

	page, err := NewClient(server.URL, server.Client()).ListYoungEvents(context.Background(), 2, 10, " 志愿 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].YoungID != "event-2" || page.Data[0].Name != "志愿服务" {
		t.Fatalf("page = %#v", page)
	}
	if page.Pagination.Page != 2 || page.Pagination.PageSize != 10 || page.Pagination.Total != 11 || page.Pagination.TotalPages != 2 {
		t.Fatalf("pagination = %#v", page.Pagination)
	}
	if page.Data[0].StartAt == nil || page.Data[0].EndAt != nil || page.Data[0].ApplyEndAt == nil {
		t.Fatalf("dates = %#v", page.Data[0])
	}
}

func TestGetYoungEventAndURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/catalog/young-events/event-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"youngId":"event-1","name":"校园分享","location":"西区","startAt":"2026-09-14T10:00:00+08:00","endAt":"2026-09-14T12:00:00+08:00","applyStartAt":"2026-09-01T00:00:00+08:00","applyEndAt":"2026-09-13T23:59:00+08:00","isActive":false
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	event, err := client.GetYoungEvent(context.Background(), " event-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if event.YoungID != "event-1" || event.Name != "校园分享" {
		t.Fatalf("event = %#v", event)
	}
	if got := client.YoungEventURL("event-1"); got != server.URL+"/catalog/young-events/event-1" {
		t.Fatalf("YoungEventURL = %q", got)
	}
}
