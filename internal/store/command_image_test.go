package store

import (
	"context"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/responses"
)

func TestCommandImageSaveIsIdempotentAndImmutable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "NapCat", UserID: "42", ConversationType: "GROUP", ConversationID: "g-1"}
	first := &responses.Image{Kind: "help", Title: "帮助", RichText: "# 帮助", AltText: "first"}
	firstID, err := s.SaveCommandImage(ctx, ident, "execution-1/image-0", first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(firstID, "img_") {
		t.Fatalf("id = %q, want img_ prefix", firstID)
	}

	second := &responses.Image{Kind: "different", Title: "替换", RichText: "# 替换", AltText: "second"}
	secondID, err := s.SaveCommandImage(ctx, ident, " execution-1/image-0 ", second)
	if err != nil {
		t.Fatal(err)
	}
	if secondID != firstID {
		t.Fatalf("idempotent save id = %q, want %q", secondID, firstID)
	}

	got, found, err := s.CommandImage(ctx, ident, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("saved image was not found")
	}
	if got == nil || got.Kind != first.Kind || got.Title != first.Title || got.RichText != first.RichText || got.AltText != first.AltText {
		t.Fatalf("stored image was replaced: %#v", got)
	}

	var count int64
	if err := s.db.Model(&commandImageRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("command image rows = %d, want 1", count)
	}
}

func TestCommandImageIsolatedByActorAndConversation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "u-42"}
	id, err := s.SaveCommandImage(ctx, ident, "execution-1/image-0", &responses.Image{AltText: "private"})
	if err != nil {
		t.Fatal(err)
	}

	for _, other := range []Identity{
		{Platform: "qqbot", UserID: "other", ConversationType: "private", ConversationID: "u-42"},
		{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "other"},
		{Platform: "qqbot", UserID: "42", ConversationType: "group", ConversationID: "u-42"},
		{Platform: "other-platform", UserID: "42", ConversationType: "private", ConversationID: "u-42"},
	} {
		got, found, err := s.CommandImage(ctx, other, id)
		if err != nil {
			t.Fatalf("lookup for %#v: %v", other, err)
		}
		if found || got != nil {
			t.Fatalf("cross-scope lookup for %#v = %#v, found=%v", other, got, found)
		}
	}

	got, found, err := s.CommandImage(ctx, ident, id)
	if err != nil || !found || got == nil || got.AltText != "private" {
		t.Fatalf("same-scope lookup = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestCommandImageSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	ident := Identity{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "u-42"}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SaveCommandImage(ctx, ident, "execution-1/image-0", &responses.Image{Kind: "weather", AltText: "晴"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, found, err := reopened.CommandImage(ctx, ident, id)
	if err != nil || !found || got == nil || got.Kind != "weather" || got.AltText != "晴" {
		t.Fatalf("reopened lookup = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestCommandImageMalformedJSONReturnsError(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := Identity{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "u-42"}
	id, err := s.SaveCommandImage(ctx, ident, "execution-1/image-0", &responses.Image{AltText: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&commandImageRow{}).Where("id = ?", id).Update("image_json", "{").Error; err != nil {
		t.Fatal(err)
	}

	got, found, err := s.CommandImage(ctx, ident, id)
	if !found || got != nil || err == nil || !strings.Contains(err.Error(), "decode command image") {
		t.Fatalf("malformed lookup = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestCommandImageValidatesInputs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := Identity{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "u-42"}

	if _, err := s.SaveCommandImage(ctx, ident, "key", nil); err == nil {
		t.Fatal("nil image was accepted")
	}
	if _, err := s.SaveCommandImage(ctx, ident, " ", &responses.Image{AltText: "image"}); err == nil {
		t.Fatal("blank key was accepted")
	}
	if _, err := s.SaveCommandImage(ctx, Identity{Platform: "qqbot", UserID: "42"}, "key", &responses.Image{AltText: "image"}); err == nil {
		t.Fatal("incomplete identity was accepted")
	}
	if _, _, err := s.CommandImage(ctx, ident, " "); err == nil {
		t.Fatal("blank id was accepted")
	}
}

func TestCommandImageSchemaVersionAndShape(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.VerifySchema(); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version = %d, want 4", version)
	}
	if !s.db.Migrator().HasTable(&commandImageRow{}) {
		t.Fatal("command_images table is missing")
	}
}

func TestCommandImageNotFound(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := Identity{Platform: "qqbot", UserID: "42", ConversationType: "private", ConversationID: "u-42"}
	got, found, err := s.CommandImage(context.Background(), ident, "img_missing")
	if err != nil || found || got != nil {
		t.Fatalf("missing lookup = %#v, found=%v, err=%v", got, found, err)
	}
}
