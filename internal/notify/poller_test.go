package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type fakePublisher struct {
	messages  []message.Outbound
	dedupe    map[string]struct{}
	failCount int
}

func (p *fakePublisher) Enqueue(_ context.Context, outbound message.Outbound) (delivery.Record, bool, error) {
	if p.failCount > 0 {
		p.failCount--
		return delivery.Record{}, false, fmt.Errorf("enqueue failed")
	}
	if p.dedupe == nil {
		p.dedupe = make(map[string]struct{})
	}
	if _, exists := p.dedupe[outbound.DedupeKey]; exists {
		return delivery.Record{}, false, nil
	}
	p.dedupe[outbound.DedupeKey] = struct{}{}
	p.messages = append(p.messages, outbound)
	return delivery.Record{Message: outbound}, true, nil
}

func TestFormatHomeworkIncludesDetails(t *testing.T) {
	homework := map[string]any{
		"title":           "Problem Set 1",
		"submissionDueAt": "2026-06-08T10:00:00+08:00",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "数据库系统"},
		},
	}
	got := formatHomework(homework)
	for _, want := range []string{"截止 𝟶𝟼-𝟶𝟾 𝟷𝟶:𝟶𝟶", "数据库系统", "Problem Set 𝟷"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatHomework missing %q: %q", want, got)
		}
	}
}

func TestFormatHomeworkFallsBackToID(t *testing.T) {
	got := formatHomework(map[string]any{"id": "hw-1"})
	if got != "hw-𝟷" {
		t.Fatalf("formatHomework fallback = %q", got)
	}
}

func TestFormatScheduleFallsBackToID(t *testing.T) {
	got := formatSchedule(map[string]any{
		"section": map[string]any{"id": "101"},
	})
	if got != "𝟷𝟶𝟷" {
		t.Fatalf("formatSchedule fallback = %q", got)
	}
}

func TestPollerSendsClassAndHomeworkOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: " PRIVATE ", ConversationID: "42"}
	overviewRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/overview" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		if r.URL.Query().Get("homeworkWindowDays") != "1" || r.URL.Query().Get("limit") != "50" {
			t.Fatalf("overview query = %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("atTime") != now.Format(time.RFC3339) {
			t.Fatalf("overview atTime = %q", r.URL.Query().Get("atTime"))
		}
		overviewRequests++
		_, _ = w.Write([]byte(`{"schedules":{"items":[{"date":"2026-06-07T08:00:00+08:00","startTime":"14:20","endTime":"15:55","section":{"id":101,"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]},"homeworks":{"items":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}}`))
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID:    "client",
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
		Resource:    server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{
		Identity:        ident,
		ClassesEnabled:  true,
		HomeworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	renderer := responses.Renderer{}
	poller := &Poller{
		Life:                 life.NewClient(server.URL, server.Client()),
		Auth:                 &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store:                db,
		Publisher:            publisher,
		Renderer:             renderer,
		Now:                  func() time.Time { return now },
		EnableImageResponses: true,
	}
	poller.tick(ctx)
	poller.tick(ctx)
	if overviewRequests != 2 {
		t.Fatalf("overviewRequests = %d, want one per tick", overviewRequests)
	}

	if len(publisher.messages) != 2 {
		t.Fatalf("messages = %#v", publisher.messages)
	}
	joined := publisher.messages[0].Content.Text + "\n" + publisher.messages[1].Content.Text
	for _, want := range []string{"课前提醒：", "作业提醒：", "数据库系统", "Problem Set 𝟷"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("messages missing %q: %#v", want, publisher.messages)
		}
	}
	for _, outbound := range publisher.messages {
		if outbound.Content.Attachment == nil || outbound.Content.Attachment.MIMEType != "image/png" || len(outbound.Content.Attachment.Data) == 0 {
			t.Fatalf("attachment = %#v", outbound.Content.Attachment)
		}
	}
	if !publisher.messages[0].ExpiresAt.Equal(now.Add(35*time.Minute)) || !publisher.messages[1].ExpiresAt.Equal(time.Date(2026, 6, 8, 10, 0, 0, 0, lifedata.ChinaLocation())) {
		t.Fatalf("expiry = %v, %v", publisher.messages[0].ExpiresAt, publisher.messages[1].ExpiresAt)
	}
}

func TestPollerRetriesFailedNotificationSend(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/homeworks" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}`))
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID:    "client",
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
		Resource:    server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{
		Identity:        ident,
		HomeworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{failCount: 1}
	poller := &Poller{
		Life:      life.NewClient(server.URL, server.Client()),
		Auth:      &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store:     db,
		Publisher: publisher,
		Now:       func() time.Time { return now },
	}
	poller.tick(ctx)
	if len(publisher.messages) != 0 {
		t.Fatalf("messages after failed enqueue = %#v", publisher.messages)
	}
	poller.tick(ctx)
	if len(publisher.messages) != 1 || !strings.Contains(publisher.messages[0].Content.Text, "作业提醒：") {
		t.Fatalf("messages after retry = %#v", publisher.messages)
	}
	if publisher.messages[0].Content.Attachment != nil {
		t.Fatalf("disabled image responses = %#v", publisher.messages[0].Content.Attachment)
	}
	poller.tick(ctx)
	if len(publisher.messages) != 1 {
		t.Fatalf("notification was enqueued again: %#v", publisher.messages)
	}
}

func TestPollerBacksOffAfterNotificationAuthFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, lifedata.ChinaLocation())
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{Identity: ident, HomeworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	poller := &Poller{
		Life: life.NewClient(server.URL, server.Client()), Auth: &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store: db, Publisher: &fakePublisher{}, Now: func() time.Time { return now },
	}

	poller.tick(ctx)
	poller.tick(ctx)
	if requests != 1 {
		t.Fatalf("requests during backoff = %d, want 1", requests)
	}
	now = now.Add(pollFailureBaseDelay)
	poller.tick(ctx)
	if requests != 2 {
		t.Fatalf("requests after backoff = %d, want 2", requests)
	}
}

func TestPollerLeavesDeliveryRetriesToOutbox(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}`))
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{Identity: ident, HomeworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	poller := &Poller{
		Life:  life.NewClient(server.URL, server.Client()),
		Auth:  &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store: db, Publisher: publisher, Now: func() time.Time { return now },
	}
	for i := 0; i < 5; i++ {
		poller.tick(ctx)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("outbox messages = %#v", publisher.messages)
	}
}

func TestPollerUsesRefreshedTokenForSchedules(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}

	var serverURL string
	scheduleRequests := 0
	refreshRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_endpoint":"` + serverURL + `/token"}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh","expires_in":3600}`))
	})
	mux.HandleFunc("/api/workspace/schedules", func(w http.ResponseWriter, r *http.Request) {
		scheduleRequests++
		dateFrom, dateTo := lifedata.DayRFC3339Range(now)
		if r.URL.Query().Get("dateFrom") != dateFrom || r.URL.Query().Get("dateTo") != dateTo || r.URL.Query().Get("limit") != "300" {
			t.Fatalf("schedule query = %q", r.URL.RawQuery)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "Bearer new-access":
			_, _ = w.Write([]byte(`{"schedules":[{"date":"2026-06-07T08:00:00+08:00","startTime":"14:20","endTime":"15:55","section":{"id":101,"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		ExpiresAt:    now.Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{
		Identity:       ident,
		ClassesEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	publisher := &fakePublisher{}
	poller := &Poller{
		Life:      life.NewClient(server.URL, server.Client()),
		Auth:      &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db, Now: func() time.Time { return now }},
		Store:     db,
		Publisher: publisher,
		Now:       func() time.Time { return now },
	}
	poller.tick(ctx)

	if refreshRequests != 1 || scheduleRequests != 2 {
		t.Fatalf("refreshRequests = %d, scheduleRequests = %d", refreshRequests, scheduleRequests)
	}
	if len(publisher.messages) != 1 || !strings.Contains(publisher.messages[0].Content.Text, "课前提醒：") {
		t.Fatalf("messages = %#v", publisher.messages)
	}
}

func TestPollerUsesRefreshedTokenForHomeworks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}

	var serverURL string
	homeworkRequests := 0
	refreshRequests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_endpoint":"` + serverURL + `/token"}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh","expires_in":3600}`))
	})
	mux.HandleFunc("/api/workspace/homeworks", func(w http.ResponseWriter, r *http.Request) {
		homeworkRequests++
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "Bearer new-access":
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}`))
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "refresh",
		ExpiresAt:    now.Add(time.Hour),
		Resource:     server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{
		Identity:        ident,
		HomeworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	publisher := &fakePublisher{}
	poller := &Poller{
		Life:      life.NewClient(server.URL, server.Client()),
		Auth:      &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db, Now: func() time.Time { return now }},
		Store:     db,
		Publisher: publisher,
		Now:       func() time.Time { return now },
	}
	poller.tick(ctx)

	if refreshRequests != 1 || homeworkRequests != 2 {
		t.Fatalf("refreshRequests = %d, homeworkRequests = %d", refreshRequests, homeworkRequests)
	}
	if len(publisher.messages) != 1 || !strings.Contains(publisher.messages[0].Content.Text, "作业提醒：") {
		t.Fatalf("messages = %#v", publisher.messages)
	}
}

func TestPollerSkipsAuthWithoutStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	publisher := &fakePublisher{}
	poller := &Poller{
		Life:      life.NewClient("https://life.example", nil),
		Auth:      &auth.Manager{},
		Store:     db,
		Publisher: publisher,
	}
	poller.tick(context.Background())
	if len(publisher.messages) != 0 {
		t.Fatalf("messages = %#v", publisher.messages)
	}
}
