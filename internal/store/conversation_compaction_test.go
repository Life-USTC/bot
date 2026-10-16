package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func conversationCompactionTestIdentity(id string) Identity {
	return Identity{
		Platform:         "napcat",
		UserID:           "user-" + id,
		ConversationType: "group",
		ConversationID:   "conversation-" + id,
	}
}

func TestConversationCompactionClaimCommitAndRelease(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := conversationCompactionTestIdentity("one")

	claimed, err := s.ClaimConversationCompaction(ctx, ident, 9, "missing-cursor", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("created a non-zero cursor without an existing compaction row")
	}
	if _, found, err := s.ConversationCompaction(ctx, ident); err != nil || found {
		t.Fatalf("non-zero claim changed row: found=%v err=%v", found, err)
	}

	firstExpiry := time.Now().UTC().Add(time.Minute)
	claimed, err = s.ClaimConversationCompaction(ctx, ident, 0, "first-worker", firstExpiry)
	if err != nil || !claimed {
		t.Fatalf("initial claim: claimed=%v err=%v", claimed, err)
	}
	claimed, err = s.ClaimConversationCompaction(ctx, ident, 0, "second-worker", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second worker acquired an active compaction claim")
	}
	if renewed, err := s.RenewConversationCompactionClaim(ctx, ident, 0, "first-worker", time.Now().Add(time.Hour)); err != nil || !renewed {
		t.Fatalf("renew summary lease: %v", err)
	}
	if renewed, err := s.RenewConversationCompactionClaim(ctx, ident, 0, "second-worker", time.Now().Add(time.Hour)); err != nil || renewed {
		t.Fatal("different worker renewed summary lease")
	}

	// A worker whose lease has expired cannot publish its model output.
	expired := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&conversationCompactionRow{}).
		Where(compactionIdentityQuery(normalizeIdentity(ident))).
		Update("claim_expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.CommitConversationCompaction(ctx, ident, 0, 7, "first-worker", "should not publish"); !errors.Is(err, ErrConversationCompactionClaimMismatch) {
		t.Fatalf("expired commit err=%v, want claim mismatch", err)
	}
	if renewed, err := s.RenewConversationCompactionClaim(ctx, ident, 0, "first-worker", time.Now().Add(time.Hour)); err != nil || renewed {
		t.Fatal("expired summary lease was revived")
	}

	claimed, err = s.ClaimConversationCompaction(ctx, ident, 0, "recovered-worker", time.Now().UTC().Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("expired claim recovery: claimed=%v err=%v", claimed, err)
	}
	if err := s.ReleaseConversationCompaction(ctx, ident, 0, "recovered-worker"); err != nil {
		t.Fatal(err)
	}
	row, found, err := s.ConversationCompaction(ctx, ident)
	if err != nil || !found {
		t.Fatalf("released row: row=%#v found=%v err=%v", row, found, err)
	}
	if row.CoveredEventID != 0 || row.Summary != "" {
		t.Fatalf("failed compaction changed committed state: %#v", row)
	}

	claimed, err = s.ClaimConversationCompaction(ctx, ident, 0, "successful-worker", time.Now().UTC().Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim after release: claimed=%v err=%v", claimed, err)
	}
	if err := s.CommitConversationCompaction(ctx, ident, 0, 7, "successful-worker", "durable summary"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitConversationCompaction(ctx, ident, 0, 8, "successful-worker", "stale summary"); !errors.Is(err, ErrConversationCompactionClaimMismatch) {
		t.Fatalf("stale commit err=%v, want claim mismatch", err)
	}
	row, found, err = s.ConversationCompaction(ctx, ident)
	if err != nil || !found || row.CoveredEventID != 7 || row.Summary != "durable summary" {
		t.Fatalf("committed row: row=%#v found=%v err=%v", row, found, err)
	}
}

func TestConversationCompactionIsolatedByConversationAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := conversationCompactionTestIdentity("first")
	second := conversationCompactionTestIdentity("second")
	if claimed, err := s.ClaimConversationCompaction(ctx, first, 0, "first-worker", time.Now().UTC().Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	if err := s.CommitConversationCompaction(ctx, first, 0, 11, "first-worker", "first summary"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimConversationCompaction(ctx, second, 0, "second-worker", time.Now().UTC().Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("second claim: claimed=%v err=%v", claimed, err)
	}
	if err := s.CommitConversationCompaction(ctx, second, 0, 3, "second-worker", "second summary"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	gotFirst, found, err := s.ConversationCompaction(ctx, first)
	if err != nil || !found || gotFirst.CoveredEventID != 11 || gotFirst.Summary != "first summary" {
		t.Fatalf("first row after restart: row=%#v found=%v err=%v", gotFirst, found, err)
	}
	gotSecond, found, err := s.ConversationCompaction(ctx, second)
	if err != nil || !found || gotSecond.CoveredEventID != 3 || gotSecond.Summary != "second summary" {
		t.Fatalf("second row after restart: row=%#v found=%v err=%v", gotSecond, found, err)
	}
}

func TestSchemaMaintenanceRejectsMissingCompactionTable(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := conversationCompactionTestIdentity("schema-preservation")
	event, created, err := s.AppendConversationEvent(ctx, ConversationEvent{
		Identity: ident, DedupeKey: "schema-preservation-event", Type: ConversationEventUser, Content: "保留这条历史",
	})
	if err != nil || !created {
		t.Fatalf("append pre-maintenance event: event=%#v created=%v err=%v", event, created, err)
	}
	if err := s.db.Migrator().DropTable(&conversationCompactionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("normal startup silently repaired the missing compaction table")
	}

	maintenance, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.PrepareSchemaForMaintenance(ctx); err == nil {
		_ = maintenance.Close()
		t.Fatal("maintenance silently repaired a malformed current database")
	}
	events, err := maintenance.ConversationEventsAfter(ctx, ident, 0, 10)
	if err != nil || len(events) != 1 || events[0].ID != event.ID || events[0].Content != event.Content {
		_ = maintenance.Close()
		t.Fatalf("pre-maintenance event changed: events=%#v err=%v", events, err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConversationEventsAfterUsesStableConversationCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	firstIdentity := conversationCompactionTestIdentity("events")
	secondIdentity := conversationCompactionTestIdentity("other")
	appendEvent := func(ident Identity, key, content string) ConversationEvent {
		event, created, err := s.AppendConversationEvent(ctx, ConversationEvent{
			Identity: ident, DedupeKey: key, Type: ConversationEventUser, Content: content,
		})
		if err != nil || !created {
			t.Fatalf("append event %s: event=%#v created=%v err=%v", key, event, created, err)
		}
		return event
	}
	first := appendEvent(firstIdentity, "event-1", "first")
	second := appendEvent(firstIdentity, "event-2", "second")
	_ = appendEvent(secondIdentity, "other-1", "other")

	page, err := s.ConversationEventsAfter(ctx, firstIdentity, 0, 1)
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("first page: page=%#v err=%v", page, err)
	}
	page, err = s.ConversationEventsAfter(ctx, firstIdentity, first.ID, 10)
	if err != nil || len(page) != 1 || page[0].ID != second.ID {
		t.Fatalf("second page: page=%#v err=%v", page, err)
	}
	if _, err := s.ConversationEventsAfter(ctx, firstIdentity, -1, 10); err == nil {
		t.Fatal("negative event cursor was accepted")
	}
}
