package life

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestListAllYoungEventsWithQueryFollowsCompletePagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/catalog/young-events" || r.URL.Query().Get("organizerId") != "org-1" || r.URL.Query().Get("dateFrom") != "2026-09-14" || r.URL.Query().Get("dateTo") != "2026-09-20" || r.URL.Query().Get("timeBasis") != "registration" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("public request carried authorization: %q", r.Header.Get("Authorization"))
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page != requests {
			t.Fatalf("page = %d, requests = %d", page, requests)
		}
		data := make([]YoungEvent, 0)
		if page == 1 {
			for i := 0; i < 100; i++ {
				data = append(data, YoungEvent{YoungID: fmt.Sprintf("event-%03d", i)})
			}
		} else {
			data = append(data, YoungEvent{YoungID: "event-100"})
		}
		_ = json.NewEncoder(w).Encode(YoungEventPage{Data: data, Pagination: YoungEventPagination{Page: page, PageSize: 100, Total: 101, TotalPages: 2}})
	}))
	defer server.Close()

	events, err := NewClient(server.URL, server.Client()).ListAllYoungEventsWithQuery(context.Background(), "", YoungEventQuery{
		OrganizerID: "org-1", DateFrom: "2026-09-14", DateTo: "2026-09-20", TimeBasis: "registration", PageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 101 || events[100].YoungID != "event-100" || requests != 2 {
		t.Fatalf("events=%d last=%#v requests=%d", len(events), events[len(events)-1], requests)
	}
}

func TestListAllYoungEventsWithQueryPreservesFreshnessMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		unknown := 2
		if page == 2 {
			unknown = 1
		}
		_ = json.NewEncoder(w).Encode(YoungEventPage{
			Data:             []YoungEvent{{YoungID: fmt.Sprintf("event-%d", page)}},
			Pagination:       YoungEventPagination{Page: page, PageSize: 1, Total: 2, TotalPages: 2},
			UnknownDateCount: unknown,
			Source:           map[string]any{"status": "fresh", "lastSyncedAt": "2026-09-15T10:00:00+08:00"},
		})
	}))
	defer server.Close()

	collection, err := NewClient(server.URL, server.Client()).ListAllYoungEventsWithQueryMetadata(context.Background(), "", YoungEventQuery{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Data) != 2 || collection.UnknownDateCount != 2 || collection.Source["status"] != "fresh" || collection.Pagination.TotalPages != 2 {
		t.Fatalf("collection = %#v", collection)
	}
}

func TestPersonalCalendarUsesShanghaiDateBoundsAndBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" || r.URL.Query().Get("dateFrom") != "2026-09-14" || r.URL.Query().Get("dateTo") != "2026-09-20" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"young-1","type":"young_event","at":"2026-09-14T10:00:00+08:00","endsAt":null,"title":"分享","location":null,"url":"/catalog/young-events/young-1","youngId":"young-1"}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
	}))
	defer server.Close()

	page, err := NewClient(server.URL, server.Client()).ListPersonalCalendarEvents(context.Background(), "token", "2026-09-14", "2026-09-20", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].Type != "young_event" || page.Data[0].YoungID != "young-1" {
		t.Fatalf("page = %#v", page)
	}
}

func TestYoungCommentTargetsPublicYoungID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/community/comments" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["targetType"] != "young-event" || payload["youngId"] != "young-1" || payload["parentId"] != "comment-1" {
			t.Fatalf("payload = %#v", payload)
		}
		_, _ = w.Write([]byte(`{"id":"comment-2"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, server.Client()).CreateYoungComment(context.Background(), "token", "young-1", "回复", "comment-1", "public", false)
	if err != nil {
		t.Fatal(err)
	}
	if result["id"] != "comment-2" {
		t.Fatalf("result = %#v", result)
	}
}

func TestYoungOrganizerURLUsesPublicCatalogPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	if got := client.YoungOrganizerURL("org-1"); got != server.URL+"/catalog/young-events/organizers/org-1" {
		t.Fatalf("YoungOrganizerURL = %q", got)
	}
	if got := client.YoungOrganizerURL(" "); got != "" {
		t.Fatalf("empty YoungOrganizerURL = %q", got)
	}
}

func TestYoungEventSubscriptionOptionsOnlySendExplicitReminders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/workspace/young-event-subscriptions/event-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["subscribed"] != true || body["remindSignup"] != false || body["remindStart"] != true {
			t.Fatalf("body = %#v", body)
		}
		if _, exists := body["remindDeadline"]; exists {
			t.Fatalf("unspecified deadline reminder was sent: %#v", body)
		}
		_, _ = w.Write([]byte(`{"youngId":"event-1","subscribed":true,"remindSignup":false,"remindDeadline":true,"remindStart":true}`))
	}))
	defer server.Close()

	falseValue, trueValue := false, true
	state, err := NewClient(server.URL, server.Client()).SetYoungEventSubscriptionWithOptions(context.Background(), "token", "event-1", true, &falseValue, nil, &trueValue)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Subscribed || state.RemindSignup || !state.RemindDeadline || !state.RemindStart {
		t.Fatalf("state = %#v", state)
	}
}

func TestYoungWorkspaceListsFollowAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("page size = %q", r.URL.Query().Get("pageSize"))
		}
		if page < 1 || page > 2 {
			t.Fatalf("unexpected page %d for %s", page, r.URL.Path)
		}
		var data any
		switch r.URL.Path {
		case "/api/workspace/young-event-subscriptions":
			items := make([]YoungEventSubscription, 100)
			if page == 2 {
				items = []YoungEventSubscription{{YoungID: "event-100"}}
			}
			data = YoungEventSubscriptionPage{Data: items, Pagination: YoungEventPagination{Page: page, PageSize: 100, Total: 101, TotalPages: 2}}
		case "/api/workspace/young-organizer-subscriptions":
			items := make([]YoungOrganizerSubscription, 100)
			if page == 2 {
				items = []YoungOrganizerSubscription{{OrganizerID: "org-100"}}
			}
			data = YoungOrganizerSubscriptionPage{Data: items, Pagination: YoungEventPagination{Page: page, PageSize: 100, Total: 101, TotalPages: 2}}
		case "/api/workspace/young-notifications":
			items := make([]YoungNotification, 100)
			if page == 2 {
				items = []YoungNotification{{ID: "notification-100"}}
			}
			data = YoungNotificationPage{Data: items, Pagination: YoungEventPagination{Page: page, PageSize: 100, Total: 101, TotalPages: 2}}
		case "/api/community/comments":
			items := make([]map[string]any, 100)
			if page == 2 {
				items = []map[string]any{{"id": "comment-100"}}
			}
			data = YoungCommentPage{Data: items, Pagination: YoungEventPagination{Page: page, PageSize: 100, Total: 101, TotalPages: 2}}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(data)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	if got, err := client.ListAllYoungEventSubscriptions(context.Background(), "token"); err != nil || len(got) != 101 {
		t.Fatalf("event subscriptions = %d, err=%v", len(got), err)
	}
	if got, err := client.ListAllYoungOrganizerSubscriptions(context.Background(), "token"); err != nil || len(got) != 101 {
		t.Fatalf("organizer subscriptions = %d, err=%v", len(got), err)
	}
	if got, err := client.ListAllYoungNotifications(context.Background(), "token", nil); err != nil || len(got) != 101 {
		t.Fatalf("notifications = %d, err=%v", len(got), err)
	}
	if got, err := client.ListAllYoungComments(context.Background(), "token", "event-1"); err != nil || len(got) != 101 {
		t.Fatalf("comments = %d, err=%v", len(got), err)
	}
}

func TestYoungCommentsLoadsReplyCursorsWithoutDuplicatingAncestors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/community/comments" {
			_, _ = w.Write([]byte(`{"data":[{"id":"root","repliesNextCursor":"next","replies":[{"id":"one","body":"first"}]}],"pagination":{"totalPages":1}}`))
			return
		}
		if r.URL.Path != "/api/community/comments/root/replies" || r.URL.Query().Get("cursor") != "next" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"thread":[{"id":"root","repliesNextCursor":null,"replies":[{"id":"one","body":"first","replies":[{"id":"two","parentId":"one","body":"second"}]}]}],"nextCursor":null}`))
	}))
	defer server.Close()
	rows, err := NewClient(server.URL, server.Client()).ListAllYoungComments(context.Background(), "token", "event")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rows)
	if len(rows) != 1 || !strings.Contains(string(encoded), `"id":"two"`) || strings.Count(string(encoded), `"id":"one"`) != 1 {
		t.Fatalf("rows=%s", encoded)
	}
}
