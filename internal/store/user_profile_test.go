package store

import (
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
