package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSaveDirectUserDisplayNameKeepsLatestNonemptyName(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := t.Context()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	first := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	if err := s.SaveDirectUserDisplayName(ctx, ident, "  Alice  ", first); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDirectUserDisplayName(ctx, ident, "Older", first.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDirectUserDisplayName(ctx, ident, "   ", first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureUser(ctx, ident); err != nil {
		t.Fatal(err)
	}
	var user userRow
	if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", "napcat", "42").Take(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != "Alice" || user.DisplayNameSeenAt == nil || !user.DisplayNameSeenAt.Equal(first) {
		t.Fatalf("profile after stale/blank updates = %#v", user)
	}
	if err := s.SaveDirectUserDisplayName(ctx, ident, "New name", first.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", "napcat", "42").Take(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != "New name" || user.DisplayNameSeenAt == nil || !user.DisplayNameSeenAt.Equal(first.Add(2*time.Hour)) {
		t.Fatalf("updated profile = %#v", user)
	}
	group := ident
	group.ConversationType = "group"
	group.ConversationID = "100"
	if err := s.SaveDirectUserDisplayName(ctx, group, "Group card", first.Add(3*time.Hour)); err == nil {
		t.Fatal("group card was accepted as a global display name")
	}
	if err := s.SaveDirectUserDisplayName(ctx, Identity{Platform: "qqbot", UserID: "42", ConversationType: "private"}, "QQ Bot user", first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", "napcat", "42").Take(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != "New name" {
		t.Fatalf("cross-platform update changed NapCat profile: %#v", user)
	}
}

func TestUserProfileMaintenanceBackfillsDirectNamesOnly(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	private := Identity{Platform: "napcat", UserID: "private-user", ConversationType: "private", ConversationID: "private-user"}
	if _, err := s.EnsureUser(ctx, private); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name string
		at   time.Time
	}{
		{"Older private name", first},
		{"Latest private name", first.Add(time.Hour)},
	} {
		if _, created, err := s.AppendConversationEvent(ctx, ConversationEvent{
			Identity: private, DedupeKey: item.name, Type: ConversationEventUser,
			ActorDisplayName: item.name, CreatedAt: item.at,
		}); err != nil || !created {
			t.Fatalf("save private event: created=%v err=%v", created, err)
		}
	}
	group := Identity{Platform: "napcat", UserID: "private-user", ConversationType: "group", ConversationID: "100"}
	if _, created, err := s.AppendConversationEvent(ctx, ConversationEvent{
		Identity: group, DedupeKey: "group-card", Type: ConversationEventUser,
		ActorDisplayName: "Group card", CreatedAt: first.Add(2 * time.Hour),
	}); err != nil || !created {
		t.Fatalf("save group event: created=%v err=%v", created, err)
	}
	groupOnly := Identity{Platform: "napcat", UserID: "group-only", ConversationType: "group", ConversationID: "100"}
	if _, err := s.EnsureUser(ctx, groupOnly); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.AppendConversationEvent(ctx, ConversationEvent{
		Identity: groupOnly, DedupeKey: "group-only", Type: ConversationEventUser,
		ActorDisplayName: "Another group card", CreatedAt: first,
	}); err != nil || !created {
		t.Fatalf("save group-only event: created=%v err=%v", created, err)
	}
	jobOnly := Identity{Platform: "napcat", UserID: "job-only", ConversationType: "private", ConversationID: "job-only"}
	if _, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: jobOnly, SourceEventID: "job-only-event",
		Input: ConversationJobInput{Data: json.RawMessage(`{"inbound":{"Actor":{"DisplayName":"Job name"}}}`)},
	}); err != nil || !created {
		t.Fatalf("save job-only name: created=%v err=%v", created, err)
	}
	for _, sql := range []string{
		"ALTER TABLE users DROP COLUMN display_name_seen_at",
		"ALTER TABLE users DROP COLUMN display_name",
		"PRAGMA user_version = 6",
	} {
		if err := s.db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("normal startup silently changed the previous database")
	}
	maintenance, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.PrepareSchemaForMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, tc := range []struct {
		userID string
		name   string
	}{
		{"private-user", "Latest private name"},
		{"job-only", "Job name"},
		{"group-only", ""},
	} {
		var user userRow
		if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", "napcat", tc.userID).Take(&user).Error; err != nil {
			t.Fatal(err)
		}
		if user.DisplayName != tc.name || (tc.name != "" && user.DisplayNameSeenAt == nil) {
			t.Fatalf("backfilled %s = %#v", tc.userID, user)
		}
	}
	if err := s.SaveDirectUserDisplayName(ctx, private, "Current name", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaForMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	var current userRow
	if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", "napcat", "private-user").Take(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.DisplayName != "Current name" {
		t.Fatalf("repeated maintenance replaced current name: %#v", current)
	}
}
