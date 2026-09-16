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
	"github.com/Life-USTC/Bot/internal/store"
)

func TestTodoRemindersFilterDedupeAndReschedule(t *testing.T) {
	now := time.Date(2026, 9, 16, 14, 0, 0, 0, lifedata.ChinaLocation())
	publisher := &fakePublisher{}
	poller := &Poller{Publisher: publisher}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	todo := func(id string, due time.Time, completed bool) map[string]any {
		return map[string]any{"id": id, "title": "写报告", "dueAt": due.Format(time.RFC3339), "completed": completed}
	}
	items := []map[string]any{todo("valid", now.Add(time.Hour), false), todo("done", now.Add(time.Hour), true), todo("past", now.Add(-time.Minute), false), todo("now", now, false), todo("later", now.Add(25*time.Hour), false), {"id": "undated", "title": "无日期"}, {"id": "invalid", "dueAt": "invalid"}}
	for i := 0; i < 2; i++ {
		if err := poller.notifyTodos(context.Background(), ident, items, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("messages = %#v", publisher.messages)
	}
	outbound := publisher.messages[0]
	if !strings.Contains(outbound.Content.TextContent(), "待办提醒") || len(outbound.Content.Parts[0].Attachment.RenderPayload) == 0 || !outbound.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("outbound = %#v", outbound)
	}
	if err := poller.notifyTodos(context.Background(), ident, []map[string]any{todo("valid", now.Add(2*time.Hour), false)}, now); err != nil {
		t.Fatal(err)
	}
	if len(publisher.messages) != 2 {
		t.Fatal("changed deadline did not get its own reminder")
	}
}

func TestHomeworkReminderRescheduledDeadline(t *testing.T) {
	now := time.Date(2026, 9, 16, 14, 0, 0, 0, lifedata.ChinaLocation())
	publisher := &fakePublisher{}
	poller := &Poller{Publisher: publisher}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	homework := map[string]any{"id": "hw", "title": "报告", "submissionDueAt": now.Add(time.Hour).Format(time.RFC3339)}
	for _, due := range []time.Time{now.Add(time.Hour), now.Add(time.Hour), now.Add(2 * time.Hour)} {
		homework["submissionDueAt"] = due.Format(time.RFC3339)
		if err := poller.notifyHomeworks(context.Background(), ident, []map[string]any{homework}, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(publisher.messages) != 2 {
		t.Fatalf("reminders = %d, want 2 deadlines", len(publisher.messages))
	}
}

func TestClassReminderAcrossMidnightUsesActualDate(t *testing.T) {
	now := time.Date(2026, 9, 16, 23, 50, 0, 0, lifedata.ChinaLocation())
	publisher := &fakePublisher{}
	poller := &Poller{Publisher: publisher}
	schedules := []map[string]any{
		{"date": "2026-09-17T00:00:00+08:00", "startTime": "00:10", "section": map[string]any{"id": 1}},
		{"date": "2026-09-16T00:00:00+08:00", "startTime": "00:10", "section": map[string]any{"id": 2}},
		{"date": "invalid", "startTime": "23:55", "section": map[string]any{"id": 3}},
	}
	if err := poller.notifyClasses(context.Background(), store.Identity{Platform: "napcat", ConversationType: "private", ConversationID: "42"}, schedules, now); err != nil {
		t.Fatal(err)
	}
	if len(publisher.messages) != 1 || !publisher.messages[0].ExpiresAt.Equal(now.Add(35*time.Minute)) {
		t.Fatalf("messages = %#v", publisher.messages)
	}
}

func TestReminderSourcesFailAndBackOffIndependently(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			now := time.Date(2026, 9, 16, 23, 50, 0, 0, lifedata.ChinaLocation())
			requests := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests[r.URL.Path]++
				switch r.URL.Path {
				case "/api/workspace/schedules":
					_, end := lifedata.DayRFC3339Range(now.Add(30 * time.Minute))
					if r.URL.Query().Get("dateTo") != end {
						t.Errorf("dateTo = %s", r.URL.Query().Get("dateTo"))
					}
					_, _ = fmt.Fprint(w, `{"schedules":[{"date":"2026-09-17T00:00:00+08:00","startTime":"00:10","section":{"id":1}}]}`)
				case "/api/workspace/homeworks":
					http.Error(w, "unavailable", status)
				case "/api/workspace/young-notifications":
					http.Error(w, "unavailable", status)
				case "/api/workspace/todos":
					query := r.URL.Query()
					if query.Get("completed") != "false" || query.Get("dueAfter") != now.Format(time.RFC3339) || query.Get("dueBefore") != now.Add(24*time.Hour).Format(time.RFC3339) {
						t.Errorf("todo query = %s", r.URL.RawQuery)
					}
					_, _ = fmt.Fprint(w, `{"todos":[{"id":"todo","title":"报告","dueAt":"2026-09-17T10:00:00+08:00","completed":false}]}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			ctx := context.Background()
			if err := db.SaveCredential(ctx, ident, store.Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL}); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveNotificationSettings(ctx, store.NotificationSettings{Identity: ident, ClassesEnabled: true, HomeworkEnabled: true, TodosEnabled: true, YoungEnabled: true}); err != nil {
				t.Fatal(err)
			}
			publisher := &fakePublisher{}
			poller := &Poller{Life: life.NewClient(server.URL, server.Client()), Auth: &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db}, Store: db, Publisher: publisher, Now: func() time.Time { return now }}
			poller.tick(ctx)
			poller.tick(ctx)
			if len(publisher.messages) != 2 {
				t.Fatalf("messages = %#v", publisher.messages)
			}
			for _, path := range []string{"/api/workspace/schedules", "/api/workspace/todos"} {
				if requests[path] != 2 {
					t.Errorf("healthy %s requests = %d", path, requests[path])
				}
			}
			for _, path := range []string{"/api/workspace/homeworks", "/api/workspace/young-notifications"} {
				if requests[path] != 1 {
					t.Errorf("failed %s requests = %d", path, requests[path])
				}
			}
			now = now.Add(pollFailureBaseDelay)
			poller.tick(ctx)
			if requests["/api/workspace/homeworks"] != 2 {
				t.Fatal("failed source did not retry")
			}
		})
	}
}

func TestReminderDedupeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().In(lifedata.ChinaLocation())
	path := t.TempDir() + "/bot.db"
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	items := []map[string]any{{"id": "todo", "title": "报告", "dueAt": now.Add(time.Hour).Format(time.RFC3339), "completed": false}}
	for i := 0; i < 2; i++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		poller := &Poller{Publisher: db}
		if err := poller.notifyTodos(ctx, ident, items, now); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if i == 1 {
			records, err := db.ClaimDue(ctx, now.Add(time.Minute), 10)
			if err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if len(records) != 1 {
				t.Fatalf("pending after restart = %d, want 1", len(records))
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
