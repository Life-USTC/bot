package notify_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/specification"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestSpecRecurringReminders(t *testing.T) {
	t.Run("bot.recurring-reminders", func(t *testing.T) {
		contract := specification.Begin(t)
		parseTime := func(value string) time.Time {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				t.Fatal(err)
			}
			return parsed
		}
		for _, scenario := range []struct {
			name, created, due string
			spec               store.RecurringReminderSpec
			restart            bool
			mutate             func(*store.Store, store.Identity, store.RecurringReminder, time.Time) error
		}{
			{name: "created", due: "2026-10-22T00:59:00Z"},
			{name: "restart-fresh", restart: true},
			{name: "expired-window", due: "2026-10-23T01:05:00Z"},
			{name: "weekly-restart", created: "2026-10-12T00:59:00Z", due: "2026-10-26T01:01:00Z", restart: true,
				spec: store.RecurringReminderSpec{Text: "周报", Frequency: "weekly", Weekday: time.Monday, TimeOfDay: "09:00"}},
			{name: "inclusive-end", spec: store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00", EndDate: "2026-10-23"}},
			{name: "edit-cancels", mutate: func(s *store.Store, ident store.Identity, rule store.RecurringReminder, now time.Time) error {
				spec := rule.RecurringReminderSpec
				spec.Text, spec.TimeOfDay = "休息", "10:00"
				_, err := s.SaveRecurringReminder(t.Context(), ident, rule.ID, spec, now)
				return err
			}},
			{name: "pause-cancels", mutate: func(s *store.Store, ident store.Identity, rule store.RecurringReminder, now time.Time) error {
				_, err := s.PauseRecurringReminder(t.Context(), ident, rule.ID, true, now)
				return err
			}},
			{name: "resume-future", mutate: func(s *store.Store, ident store.Identity, rule store.RecurringReminder, now time.Time) error {
				if _, err := s.PauseRecurringReminder(t.Context(), ident, rule.ID, true, now); err != nil {
					return err
				}
				_, err := s.PauseRecurringReminder(t.Context(), ident, rule.ID, false, now)
				return err
			}},
			{name: "notification-off-on", mutate: func(s *store.Store, ident store.Identity, _ store.RecurringReminder, _ time.Time) error {
				handler := commands.Handler{Store: s}
				for _, text := range []string{"通知 关", "通知 开"} {
					if _, handled := handler.Handle(t.Context(), commands.Input{Identity: ident, Text: text}); !handled {
						return fmt.Errorf("command %q was not handled", text)
					}
				}
				return nil
			}},
			{name: "delete-cancels", mutate: func(s *store.Store, ident store.Identity, rule store.RecurringReminder, _ time.Time) error {
				return s.DeleteRecurringReminder(t.Context(), ident, rule.ID)
			}},
		} {
			if scenario.created == "" {
				scenario.created = "2026-10-22T00:59:00Z"
			}
			if scenario.due == "" {
				scenario.due = "2026-10-23T01:01:00Z"
			}
			if scenario.spec.Text == "" {
				scenario.spec = store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}
			}
			path := filepath.Join(t.TempDir(), "recurring.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			ident := store.Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
			rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, scenario.spec, parseTime(scenario.created))
			if err != nil {
				t.Fatal(err)
			}
			if scenario.restart {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = s.Close() })
			now := parseTime(scenario.due)
			for range 2 {
				if err := s.EnqueueDueRecurringReminders(t.Context(), now); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.mutate != nil {
				if err := scenario.mutate(s, ident, rule, now); err != nil {
					t.Fatal(err)
				}
			}
			rules, err := s.RecurringReminders(t.Context(), ident)
			if err != nil {
				t.Fatal(err)
			}
			observed := specification.RecurringReminder{Rules: len(rules), NextAt: []string{}, Texts: []string{}}
			for _, rule := range rules {
				if rule.Paused {
					observed.Paused++
				}
				if rule.NextAt != nil {
					observed.NextAt = append(observed.NextAt, rule.NextAt.UTC().Format(time.RFC3339))
				}
			}
			deliveries, err := s.ClaimDue(t.Context(), now, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range deliveries {
				observed.Texts = append(observed.Texts, record.Message.Content.TextContent())
				if record.Message.TextPolicy == message.TextPolicyImageOnly {
					observed.ImageOnly++
				}
			}
			contract.Check(scenario.name, observed)
		}
	})
}
