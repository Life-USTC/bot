package store

import (
	"context"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/specification"
	"strings"
	"testing"
	"time"
)

func rainFixture(t *testing.T) (*Store, RainSettings, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/rain.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	settings, err := s.RainSettings(t.Context(), ident)
	if err != nil || settings.Enabled || settings.Campus != "ustc-main" {
		t.Fatalf("defaults=%+v err=%v", settings, err)
	}
	settings.Enabled = true
	if err := s.SaveRainSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	settings, err = s.RainSettings(t.Context(), ident)
	if err != nil {
		t.Fatal(err)
	}
	return s, settings, time.Now().UTC().Truncate(time.Second)
}
func observeRain(t *testing.T, s *Store, setting RainSettings, session string, observed time.Time, rain bool, now time.Time) {
	t.Helper()
	if err := s.ObserveRain(t.Context(), setting, session, observed, rain, now); err != nil {
		t.Fatal(err)
	}
}
func rainCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var count int64
	if err := s.db.Model(&outgoingMessageRow{}).Where("kind = ?", "notification.rain").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}
func TestRainTransitionsAreAtomicAndDeduplicated(t *testing.T) {
	s, setting, now := rainFixture(t)
	observeRain(t, s, setting, "run", now.Add(-30*time.Minute), false, now)
	if rainCount(t, s) != 0 {
		t.Fatal("first snapshot notified")
	}
	observeRain(t, s, setting, "run", now.Add(-20*time.Minute), true, now)
	observeRain(t, s, setting, "run", now.Add(-20*time.Minute), true, now)
	observeRain(t, s, setting, "run", now.Add(-10*time.Minute), true, now)
	if rainCount(t, s) != 1 {
		t.Fatal("ongoing rain duplicated start")
	}
	start, err := s.ClaimDue(t.Context(), now, 10)
	if err != nil || len(start) != 1 {
		t.Fatalf("start=%+v err=%v", start, err)
	}
	text := start[0].Message.Content.TextContent()
	if !strings.Contains(text, "开始下雨") || !strings.Contains(text, "本部") || !strings.Contains(text, "观测时间：") || start[0].Message.Target.ID != "42" {
		t.Fatal(text)
	}
	// A new stop invalidates a claimed start before it reaches the adapter.
	observeRain(t, s, setting, "run", now, false, now)
	if allowed, err := s.CanDeliver(t.Context(), start[0].ID); err != nil || allowed {
		t.Fatalf("superseded start allowed=%v err=%v", allowed, err)
	}
	stop, err := s.ClaimDue(t.Context(), now, 10)
	if err != nil || len(stop) != 1 || !strings.Contains(stop[0].Message.Content.TextContent(), "雨停") {
		t.Fatalf("stop=%+v err=%v", stop, err)
	}
	if err := s.Complete(t.Context(), stop[0].ID, delivery.Outcome{State: delivery.OutcomeRetryable}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	observeRain(t, s, setting, "run", now, false, now)
	if rainCount(t, s) != 2 {
		t.Fatal("retry made a new outbox record")
	}
	retry, err := s.ClaimDue(t.Context(), now.Add(time.Second), 10)
	if err != nil || len(retry) != 1 || retry[0].ID != stop[0].ID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
}
func TestRainEnqueueFailureRollsBackObservation(t *testing.T) {
	s, setting, now := rainFixture(t)
	observeRain(t, s, setting, "run", now.Add(-time.Minute), false, now)
	if err := s.db.Exec("CREATE TRIGGER fail_rain BEFORE INSERT ON outgoing_messages BEGIN SELECT RAISE(ABORT, 'injected outbox failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRain(t.Context(), setting, "run", now, true, now); err == nil {
		t.Fatal("expected outbox failure")
	}
	if err := s.db.Exec("DROP TRIGGER fail_rain").Error; err != nil {
		t.Fatal(err)
	}
	observeRain(t, s, setting, "run", now, true, now)
	if rainCount(t, s) != 1 {
		t.Fatal("failed enqueue advanced the baseline")
	}
}
func TestRainRecoveryAndReplayOnlyRebaseline(t *testing.T) {
	for _, kind := range []string{"failure", "older", "conflicting", "long-gap"} {
		t.Run(kind, func(t *testing.T) {
			s, setting, now := rainFixture(t)
			observeRain(t, s, setting, "run", now.Add(-20*time.Minute), false, now)
			switch kind {
			case "failure":
				observeRain(t, s, setting, "", time.Time{}, false, now)
			case "older":
				observeRain(t, s, setting, "run", now.Add(-30*time.Minute), true, now)
			case "conflicting":
				observeRain(t, s, setting, "run", now.Add(-20*time.Minute), true, now)
			case "long-gap":
				now = now.Add(time.Hour)
			}
			observeRain(t, s, setting, "run", now, true, now)
			if rainCount(t, s) != 0 {
				t.Fatal("recovery reported an old transition")
			}
			observeRain(t, s, setting, "run", now.Add(time.Minute), false, now.Add(time.Minute))
			if rainCount(t, s) != 1 {
				t.Fatal("next actual transition not delivered")
			}
		})
	}
}
func TestRainSettingsInvalidateOldPollAndQueuedMessages(t *testing.T) {
	for _, kind := range []string{"off", "campus", "conversation"} {
		t.Run(kind, func(t *testing.T) {
			s, setting, now := rainFixture(t)
			observeRain(t, s, setting, "run", now.Add(-time.Minute), false, now)
			observeRain(t, s, setting, "run", now, true, now)
			next := setting
			if kind == "off" {
				next.Enabled = false
			} else {
				next.Campus = "ustc-gaoxin"
			}
			if kind == "conversation" {
				next.Identity.ConversationType = "guild_private"
				next.Identity.ConversationID = "direct-session-2"
			}
			if err := s.SaveRainSettings(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			observeRain(t, s, setting, "run", now.Add(time.Minute), false, now.Add(time.Minute))
			claimed, err := s.ClaimDue(t.Context(), now, 10)
			if err != nil || len(claimed) != 0 {
				t.Fatalf("old settings produced delivery: %+v %v", claimed, err)
			}
			next, err = s.RainSettings(t.Context(), setting.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "campus" {
				observeRain(t, s, next, "run", now.Add(time.Minute), true, now.Add(time.Minute))
				if rainCount(t, s) != 1 {
					t.Fatal("new campus initial state notified")
				}
			}
		})
	}
}
func TestRainRestartPreservesPreferencesButInvalidatesPending(t *testing.T) {
	path := t.TempDir() + "/rain.db"
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setting := RainSettings{Identity: Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}, Enabled: true, Campus: "ustc-gaoxin"}
	if err := s.SaveRainSettings(ctx, setting); err != nil {
		t.Fatal(err)
	}
	setting, err = s.RainSettings(ctx, setting.Identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	observeRain(t, s, setting, "before", now.Add(-time.Minute), false, now)
	observeRain(t, s, setting, "before", now, true, now)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	saved, err := s.RainSettings(ctx, setting.Identity)
	if err != nil || !saved.Enabled || saved.Campus != "ustc-gaoxin" {
		t.Fatalf("settings=%+v %v", saved, err)
	}
	records, err := s.ClaimDue(ctx, now, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("startup replay=%+v %v", records, err)
	}
	observeRain(t, s, saved, "after", now.Add(time.Minute), false, now.Add(time.Minute))
	if rainCount(t, s) != 1 {
		t.Fatal("restart notified stale rain end")
	}
}
func TestRainRejectsGroupAndOldDatabaseWithoutMutation(t *testing.T) {
	s, setting, _ := rainFixture(t)
	setting.Identity.ConversationType = "group"
	if err := s.SaveRainSettings(t.Context(), setting); err == nil {
		t.Fatal("group reminders accepted")
	}
	if err := s.db.Exec("PRAGMA user_version = 7").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaForMaintenance(t.Context()); err == nil {
		t.Fatal("previous schema migrated implicitly")
	}
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil || version != 7 {
		t.Fatalf("version=%v err=%v", version, err)
	}
}

func TestSpecRainTransitions(t *testing.T) {
	t.Run("bot.rain-transitions", func(t *testing.T) {
		contract := specification.Begin(t)
		s, setting, now := rainFixture(t)
		check := func(name string) {
			var pending int64
			if err := s.db.Model(&outgoingMessageRow{}).Where("kind = ? AND status = ?", "notification.rain", string(delivery.StatusPending)).Count(&pending).Error; err != nil {
				t.Fatal(err)
			}
			saved, err := s.RainSettings(t.Context(), setting.Identity)
			if err != nil {
				t.Fatal(err)
			}
			contract.Check(name, specification.RainTransition{Queued: int(rainCount(t, s)), Pending: int(pending), Enabled: saved.Enabled, Campus: saved.Campus})
		}
		observeRain(t, s, setting, "run", now.Add(-3*time.Minute), false, now)
		check("baseline")
		observeRain(t, s, setting, "run", now.Add(-2*time.Minute), true, now)
		check("start")
		observeRain(t, s, setting, "run", now.Add(-2*time.Minute), true, now)
		check("same-snapshot")
		observeRain(t, s, setting, "run", now.Add(-time.Minute), false, now)
		check("stop")
		observeRain(t, s, setting, "", time.Time{}, false, now)
		observeRain(t, s, setting, "run", now, true, now)
		check("recovery")
		setting.Enabled = false
		if err := s.SaveRainSettings(t.Context(), setting); err != nil {
			t.Fatal(err)
		}
		check("disabled")
	})
}
