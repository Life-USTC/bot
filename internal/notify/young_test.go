package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestYoungNotificationReadFollowsDurableOutboxAndDedupes(t *testing.T) {
	readRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/workspace/young-notifications/n-1/read" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		readRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	publisher := &fakePublisher{failCount: 1}
	poller := &Poller{Life: life.NewClient(server.URL, server.Client()), Publisher: publisher}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, lifedata.ChinaLocation())
	notifications := []life.YoungNotification{{ID: "n-1", YoungID: "event-1", OrganizerID: "org-1", Title: "报名开放", Body: "活动详情", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
	ident := store.Identity{Platform: "test", UserID: "u", ConversationType: "private", ConversationID: "u"}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", notifications, now); err == nil {
		t.Fatal("first enqueue unexpectedly succeeded")
	}
	if readRequests != 0 || len(publisher.messages) != 0 {
		t.Fatalf("failed enqueue read=%d messages=%d", readRequests, len(publisher.messages))
	}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", notifications, now); err != nil {
		t.Fatal(err)
	}
	if readRequests != 1 || len(publisher.messages) != 1 || !strings.Contains(publisher.messages[0].Content.TextContent(), "活动详情") || !strings.Contains(publisher.messages[0].Content.TextContent(), "/catalog/young-events/event-1") || !strings.Contains(publisher.messages[0].Content.TextContent(), "/catalog/young-events/organizers/org-1") {
		t.Fatalf("retry read=%d messages=%#v", readRequests, publisher.messages)
	}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", notifications, now); err != nil {
		t.Fatal(err)
	}
	if readRequests != 2 || len(publisher.messages) != 1 {
		t.Fatalf("duplicate read=%d messages=%d", readRequests, len(publisher.messages))
	}
}

func TestYoungNotificationExpiryDoesNotMarkRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("expired notification made request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	poller := &Poller{Life: life.NewClient(server.URL, server.Client()), Publisher: &fakePublisher{}}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "test", UserID: "u", ConversationType: "private", ConversationID: "u"}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", []life.YoungNotification{{ID: "expired", ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339)}}, now); err != nil {
		t.Fatal(err)
	}
}

func TestYoungNotificationReadFailureRetriesWithoutDuplicateEnqueue(t *testing.T) {
	readRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/young-notifications/n-1/read" {
			t.Fatalf("request = %s", r.URL.Path)
		}
		readRequests++
		if readRequests == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	poller := &Poller{Life: life.NewClient(server.URL, server.Client()), Publisher: &fakePublisher{}}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, lifedata.ChinaLocation())
	notifications := []life.YoungNotification{{ID: "n-1", Title: "提醒", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
	ident := store.Identity{Platform: "test", UserID: "u", ConversationType: "private", ConversationID: "u"}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", notifications, now); err == nil {
		t.Fatal("first read unexpectedly succeeded")
	}
	if err := poller.notifyYoungNotifications(context.Background(), ident, "token", notifications, now); err != nil {
		t.Fatal(err)
	}
	if readRequests != 2 || len(poller.Publisher.(*fakePublisher).messages) != 1 {
		t.Fatalf("readRequests=%d messages=%d", readRequests, len(poller.Publisher.(*fakePublisher).messages))
	}
}

func TestYoungNotificationForbiddenBacksOffOnlyItsSource(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/young-notifications" {
			t.Fatalf("request = %s", r.URL.Path)
		}
		http.Error(w, "scope missing", http.StatusForbidden)
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "test", UserID: "u", ConversationType: "private", ConversationID: "u"}
	if err := db.SaveCredential(ctx, ident, store.Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{Identity: ident, YoungEnabled: true}); err != nil {
		t.Fatal(err)
	}
	poller := &Poller{
		Life:  life.NewClient(server.URL, server.Client()),
		Auth:  &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store: db, Publisher: &fakePublisher{},
	}
	if got := poller.notifyUser(ctx, store.NotificationSettings{Identity: ident, YoungEnabled: true}); got != pollSucceeded {
		t.Fatalf("notifyUser result = %v, want successful independent polling", got)
	}
}
