package commands

import (
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"testing"
)

func TestImageRegistrationKeepsSnapshotAlignedWithRecoveredRead(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	source := Response{Parts: []Response{{Image: &responses.Image{Kind: "bus", Title: "08:00"}}}}
	first := source
	if err := RegisterResponseImages(t.Context(), db, ident, "execution", &first); err != nil {
		t.Fatal(err)
	}
	if len(source.Parts[0].Images) != 0 {
		t.Fatal("modified a shared cached response")
	}
	replay := source
	if err := RegisterResponseImages(t.Context(), db, ident, "execution", &replay); err != nil {
		t.Fatal(err)
	}
	if first.Images[0].ID != replay.Images[0].ID {
		t.Fatal("unchanged image ID changed on replay")
	}
	refreshed := Response{Parts: []Response{{Image: &responses.Image{Kind: "bus", Title: "09:00"}}}}
	if err := RegisterResponseImages(t.Context(), db, ident, "execution", &refreshed); err != nil {
		t.Fatal(err)
	}
	if first.Images[0].ID == refreshed.Images[0].ID {
		t.Fatal("refreshed read reused a stale image")
	}
	for _, item := range []struct {
		response Response
		title    string
	}{{first, "08:00"}, {refreshed, "09:00"}} {
		image, found, err := db.CommandImage(t.Context(), ident, item.response.Images[0].ID)
		if err != nil || !found || image.Title != item.title {
			t.Fatalf("image=%#v found=%v err=%v", image, found, err)
		}
	}
}
