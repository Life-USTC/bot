package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestRecurringPollerEnqueuesWithoutOAuthOrCampusClient(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/recurring.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
	now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
	if _, err := s.SaveRecurringReminder(t.Context(), ident, 0, store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, now); err != nil {
		t.Fatal(err)
	}
	due := now.Add(time.Minute)
	poller := RecurringPoller{Store: s, Now: func() time.Time { return due }}
	poller.tick(t.Context())
	poller.tick(t.Context())
	rows, err := s.ClaimDue(t.Context(), due, 10)
	if err != nil || len(rows) != 1 || rows[0].Message.Kind != "notification.recurring" || !strings.Contains(rows[0].Message.Content.TextContent(), "喝水") {
		t.Fatalf("outbox=%#v err=%v", rows, err)
	}
}
