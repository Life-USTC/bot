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
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type fakeSender struct {
	messages  []string
	images    []*responses.Image
	failCount int
}

func (s *fakeSender) SendMessage(ctx context.Context, ident store.Identity, message string) error {
	if s.failCount > 0 {
		s.failCount--
		return fmt.Errorf("send failed")
	}
	s.messages = append(s.messages, message)
	return nil
}

func (s *fakeSender) SendRichMessage(ctx context.Context, ident store.Identity, message string, image *responses.Image) error {
	if s.failCount > 0 {
		s.failCount--
		return fmt.Errorf("send failed")
	}
	s.messages = append(s.messages, message)
	s.images = append(s.images, image)
	return nil
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case "/api/schedules":
			if r.URL.Query().Get("sectionId") != "101" {
				t.Fatalf("sectionId = %q", r.URL.Query().Get("sectionId"))
			}
			_, _ = w.Write([]byte(`{"data":[{"date":"2026-06-07T08:00:00+08:00","startTime":"14:20","endTime":"15:55","section":{"id":101,"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
		case "/api/me/subscriptions/homeworks":
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
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
	sender := &fakeSender{}
	poller := &Poller{
		Life:                 life.NewClient(server.URL, server.Client()),
		Auth:                 &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store:                db,
		Sender:               sender,
		Now:                  func() time.Time { return now },
		EnableImageResponses: true,
	}
	poller.tick(ctx)
	poller.tick(ctx)

	if len(sender.messages) != 2 {
		t.Fatalf("messages = %#v", sender.messages)
	}
	joined := strings.Join(sender.messages, "\n")
	for _, want := range []string{"课前提醒：", "作业提醒：", "数据库系统", "Problem Set 𝟷"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("messages missing %q: %#v", want, sender.messages)
		}
	}
	if len(sender.images) != 2 || sender.images[0] == nil || sender.images[0].Kind != "class_reminder" || sender.images[1] == nil || sender.images[1].Kind != "homework_reminder" {
		t.Fatalf("images = %#v", sender.images)
	}
	if !strings.Contains(sender.images[0].RichText, "| 地点 | 时间 | 课程 |") || !strings.Contains(sender.images[0].RichText, "| 西区 3A204 | 14:20-15:55 | 数据库系统 |") {
		t.Fatalf("class reminder rich text = %q", sender.images[0].RichText)
	}
	if !strings.Contains(sender.images[1].RichText, "| 截止 | 课程 | 作业 |") || !strings.Contains(sender.images[1].RichText, "| 06-08 10:00 | 数据库系统 | Problem Set 1 |") {
		t.Fatalf("homework reminder rich text = %q", sender.images[1].RichText)
	}
	renderer := responses.Renderer{}
	for _, image := range sender.images {
		if _, _, _, err := renderer.RenderPNG(image); err != nil {
			t.Fatalf("render %s notification: %v", image.Kind, err)
		}
	}
}

func TestPollerRetriesFailedNotificationSend(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, lifedata.ChinaLocation())
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me/subscriptions/homeworks":
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-08T10:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null}]}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
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
	sender := &fakeSender{failCount: 1}
	poller := &Poller{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store:  db,
		Sender: sender,
		Now:    func() time.Time { return now },
	}
	poller.tick(ctx)
	if len(sender.messages) != 0 {
		t.Fatalf("messages after failed send = %#v", sender.messages)
	}
	poller.tick(ctx)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "作业提醒：") {
		t.Fatalf("messages after retry = %#v", sender.messages)
	}
	if len(sender.images) != 1 || sender.images[0] != nil {
		t.Fatalf("disabled image responses = %#v, want one nil image", sender.images)
	}
	poller.tick(ctx)
	if len(sender.messages) != 1 {
		t.Fatalf("notification was sent again: %#v", sender.messages)
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
	mux.HandleFunc("/api/calendar-subscriptions/current", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "Bearer new-access":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	})
	mux.HandleFunc("/api/schedules", func(w http.ResponseWriter, r *http.Request) {
		scheduleRequests++
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			t.Fatal("schedule request used stale token")
		case "Bearer new-access":
			_, _ = w.Write([]byte(`{"data":[{"date":"2026-06-07T08:00:00+08:00","startTime":"14:20","endTime":"15:55","section":{"id":101,"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
		default:
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
	})
	mux.HandleFunc("/api/me/subscriptions/homeworks", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			t.Fatal("homework request used stale token")
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
		ClassesEnabled:  true,
		HomeworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	sender := &fakeSender{}
	poller := &Poller{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db, Now: func() time.Time { return now }},
		Store:  db,
		Sender: sender,
		Now:    func() time.Time { return now },
	}
	poller.tick(ctx)

	if refreshRequests != 1 || scheduleRequests != 1 {
		t.Fatalf("refreshRequests = %d, scheduleRequests = %d", refreshRequests, scheduleRequests)
	}
	if len(sender.messages) != 2 || !strings.Contains(strings.Join(sender.messages, "\n"), "课前提醒：") || !strings.Contains(strings.Join(sender.messages, "\n"), "作业提醒：") {
		t.Fatalf("messages = %#v", sender.messages)
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
	mux.HandleFunc("/api/me/subscriptions/homeworks", func(w http.ResponseWriter, r *http.Request) {
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

	sender := &fakeSender{}
	poller := &Poller{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db, Now: func() time.Time { return now }},
		Store:  db,
		Sender: sender,
		Now:    func() time.Time { return now },
	}
	poller.tick(ctx)

	if refreshRequests != 1 || homeworkRequests != 2 {
		t.Fatalf("refreshRequests = %d, homeworkRequests = %d", refreshRequests, homeworkRequests)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "作业提醒：") {
		t.Fatalf("messages = %#v", sender.messages)
	}
}

func TestPollerSkipsAuthWithoutStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	sender := &fakeSender{}
	poller := &Poller{
		Life:   life.NewClient("https://life.example", nil),
		Auth:   &auth.Manager{},
		Store:  db,
		Sender: sender,
	}
	poller.tick(context.Background())
	if len(sender.messages) != 0 {
		t.Fatalf("messages = %#v", sender.messages)
	}
}
