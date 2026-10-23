package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
)

func TestRecurringReminderNextOccurrenceUsesLocalClockAndEndDate(t *testing.T) {
	for _, tt := range []struct {
		name  string
		spec  RecurringReminderSpec
		after string
		want  string
	}{
		{"daily default zone", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, "2026-10-23T00:59:00Z", "2026-10-23T01:00:00Z"},
		{"strictly future", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, "2026-10-23T01:00:00Z", "2026-10-24T01:00:00Z"},
		{"weekly", RecurringReminderSpec{Text: "周报", Frequency: "weekly", Weekday: time.Monday, TimeOfDay: "09:00"}, "2026-10-23T01:00:00Z", "2026-10-26T01:00:00Z"},
		{"inclusive local end", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00", EndDate: "2026-10-23"}, "2026-10-23T00:59:00Z", "2026-10-23T01:00:00Z"},
		{"ended", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00", EndDate: "2026-10-23"}, "2026-10-23T01:00:00Z", ""},
		{"DST gap skips date", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "02:30", Timezone: "America/New_York"}, "2027-03-14T05:00:00Z", "2027-03-15T06:30:00Z"},
		{"DST fold first occurrence", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "01:30", Timezone: "America/New_York"}, "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"DST fold does not repeat", RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "01:30", Timezone: "America/New_York"}, "2026-11-01T05:31:00Z", "2026-11-02T06:30:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := normalizeRecurringReminderSpec(tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			after, err := time.Parse(time.RFC3339, tt.after)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextRecurringReminderAt(spec, after)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("ended reminder has next occurrence %s", got)
				}
				return
			}
			if got == nil || got.UTC().Format(time.RFC3339) != tt.want {
				t.Fatalf("next=%v, want %s", got, tt.want)
			}
		})
	}
}

func TestRecurringReminderOutboxSurvivesRestartWithoutDuplicates(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "reminders.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
	now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
	rule, err := s.SaveRecurringReminder(ctx, ident, 0, RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, now)
	if err != nil {
		t.Fatal(err)
	}
	due := now.Add(90 * time.Second)
	for range 2 {
		if err := s.EnqueueDueRecurringReminders(ctx, due); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnqueueDueRecurringReminders(ctx, due); err != nil {
		t.Fatal(err)
	}
	rows := recurringOutboxRows(t, s)
	if len(rows) != 1 || rows[0].Status != string(delivery.StatusPending) || rows[0].ExpiresAt == nil || !rows[0].ExpiresAt.Equal(now.Add(6*time.Minute)) {
		t.Fatalf("outbox=%#v", rows)
	}
	after, err := s.RecurringReminder(ctx, ident, rule.ID)
	if err != nil || after.NextAt == nil || !after.NextAt.Equal(now.Add(24*time.Hour+time.Minute)) {
		t.Fatalf("rule=%#v err=%v", after, err)
	}
}

func TestRecurringReminderSkipsStaleHistoryAndStopsAtEndDate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ident := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
	now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
	rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00", EndDate: "2026-10-24"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDueRecurringReminders(t.Context(), now.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows := recurringOutboxRows(t, s); len(rows) != 0 {
		t.Fatalf("replayed stale reminders: %#v", rows)
	}
	after, err := s.RecurringReminder(t.Context(), ident, rule.ID)
	if err != nil || after.NextAt != nil {
		t.Fatalf("ended rule=%#v err=%v", after, err)
	}
}

func TestRecurringReminderRestartKeepsFreshOccurrenceAfterStaleWatermark(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ident := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
	created := time.Date(2026, 10, 22, 0, 59, 0, 0, time.UTC)
	rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, created)
	if err != nil {
		t.Fatal(err)
	}
	restarted := time.Date(2026, 10, 23, 1, 1, 0, 0, time.UTC)
	for range 2 {
		if err := s.EnqueueDueRecurringReminders(t.Context(), restarted); err != nil {
			t.Fatal(err)
		}
	}
	rows := recurringOutboxRows(t, s)
	if len(rows) != 1 || rows[0].ExpiresAt == nil || !rows[0].ExpiresAt.Equal(restarted.Add(4*time.Minute)) {
		t.Fatalf("restart did not retain exactly today's fresh occurrence: %#v", rows)
	}
	after, err := s.RecurringReminder(t.Context(), ident, rule.ID)
	if err != nil || after.NextAt == nil || !after.NextAt.Equal(time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("restart watermark=%#v err=%v", after, err)
	}
}

func TestRecurringReminderMutationsCancelOldDeliveryAndRespectOwner(t *testing.T) {
	for _, action := range []string{"edit", "pause", "delete", "all-off"} {
		t.Run(action, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "reminders.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ctx := t.Context()
			alice := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
			bob := Identity{Platform: "napcat", UserID: "bob", ConversationType: "private", ConversationID: "bob"}
			now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
			spec := RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}
			rule, err := s.SaveRecurringReminder(ctx, alice, 0, spec, now)
			if err != nil {
				t.Fatal(err)
			}
			other, err := s.SaveRecurringReminder(ctx, bob, 0, spec, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecurringReminder(ctx, bob, rule.ID); !errors.Is(err, ErrRecurringReminderNotFound) {
				t.Fatalf("other owner read: %v", err)
			}
			if err := s.DeleteRecurringReminder(ctx, bob, rule.ID); !errors.Is(err, ErrRecurringReminderNotFound) {
				t.Fatalf("other owner delete: %v", err)
			}
			due := now.Add(90 * time.Second)
			if err := s.EnqueueDueRecurringReminders(ctx, due); err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimDue(ctx, due, 10)
			if err != nil || len(claimed) != 2 {
				t.Fatalf("claim=%#v err=%v", claimed, err)
			}
			switch action {
			case "edit":
				spec.Text = "修改后的内容"
				_, err = s.SaveRecurringReminder(ctx, alice, rule.ID, spec, due)
			case "pause":
				_, err = s.PauseRecurringReminder(ctx, alice, rule.ID, true, due)
			case "delete":
				err = s.DeleteRecurringReminder(ctx, alice, rule.ID)
			case "all-off":
				err = s.PauseAllRecurringReminders(ctx, alice)
			}
			if err != nil {
				t.Fatal(err)
			}
			rows := recurringOutboxRows(t, s)
			if len(rows) != 2 || rows[0].Status != string(delivery.StatusExpired) || rows[1].Status != string(delivery.StatusDelivering) {
				t.Fatalf("owner cancellation=%#v", rows)
			}
			otherAfter, err := s.RecurringReminder(ctx, bob, other.ID)
			if err != nil || otherAfter.Paused {
				t.Fatalf("other owner's rule changed: %#v err=%v", otherAfter, err)
			}
			if action == "pause" || action == "all-off" {
				resumed, err := s.PauseRecurringReminder(ctx, alice, rule.ID, false, due)
				if err != nil || resumed.Paused || resumed.NextAt == nil || !resumed.NextAt.After(due) {
					t.Fatalf("resume=%#v err=%v", resumed, err)
				}
			}
		})
	}
}

func TestRecurringReminderWeeklyRestartAndFreshnessBoundary(t *testing.T) {
	for _, delay := range []time.Duration{time.Minute, 5*time.Minute - time.Nanosecond, 5 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "reminders.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ident := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
			created := time.Date(2026, 10, 19, 0, 59, 0, 0, time.UTC)
			rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, RecurringReminderSpec{Text: "写周报", Frequency: "weekly", Weekday: time.Monday, TimeOfDay: "09:00"}, created)
			if err != nil {
				t.Fatal(err)
			}
			occurrence := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
			if err := s.EnqueueDueRecurringReminders(t.Context(), occurrence.Add(delay)); err != nil {
				t.Fatal(err)
			}
			wanted := 1
			if delay >= RecurringReminderFreshness {
				wanted = 0
			}
			if rows := recurringOutboxRows(t, s); len(rows) != wanted {
				t.Fatalf("window admitted %d rows, want %d", len(rows), wanted)
			}
			after, err := s.RecurringReminder(t.Context(), ident, rule.ID)
			if err != nil || after.NextAt == nil || !after.NextAt.Equal(occurrence.AddDate(0, 0, 7)) {
				t.Fatalf("weekly watermark=%#v err=%v", after, err)
			}
		})
	}
}

func TestRecurringReminderStateAndOutboxRollbackTogether(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ident := Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
	now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
	rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("CREATE TRIGGER reject_recurring_outbox BEFORE INSERT ON outgoing_messages BEGIN SELECT RAISE(ABORT, 'injected outbox failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDueRecurringReminders(t.Context(), now.Add(90*time.Second)); err == nil {
		t.Fatal("injected outbox failure succeeded")
	}
	after, err := s.RecurringReminder(t.Context(), ident, rule.ID)
	if err != nil || after.NextAt == nil || !after.NextAt.Equal(*rule.NextAt) || len(recurringOutboxRows(t, s)) != 0 {
		t.Fatalf("failed enqueue advanced rule: %#v err=%v", after, err)
	}
}

func recurringOutboxRows(t *testing.T, s *Store) []outgoingMessageRow {
	t.Helper()
	var rows []outgoingMessageRow
	if err := s.db.WithContext(context.Background()).Where("kind = ?", "notification.recurring").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRecurringReminderRejectsInvalidRules(t *testing.T) {
	for _, spec := range []RecurringReminderSpec{
		{Text: "", Frequency: "daily", TimeOfDay: "09:00"},
		{Text: "提醒", Frequency: "monthly", TimeOfDay: "09:00"},
		{Text: "提醒", Frequency: "daily", TimeOfDay: "25:00"},
		{Text: "提醒", Frequency: "daily", TimeOfDay: "9:00"},
		{Text: "提醒", Frequency: "weekly", Weekday: 8, TimeOfDay: "09:00"},
		{Text: "提醒", Frequency: "daily", TimeOfDay: "09:00", Timezone: "missing/timezone"},
		{Text: "提醒", Frequency: "daily", TimeOfDay: "09:00", EndDate: "2026-02-30"},
	} {
		if _, err := normalizeRecurringReminderSpec(spec); err == nil {
			t.Errorf("accepted invalid rule: %#v", spec)
		}
	}
}
