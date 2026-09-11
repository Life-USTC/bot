package botapp

import (
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
)

func TestRoomMapPresentationKeepsDescriptionWithURLAttachment(t *testing.T) {
	content, err := (&Coordinator{}).presentationContent(t.Context(), commands.Response{
		Text:  "3A204：三教 2",
		Image: &responses.Image{Kind: "room-map", URL: "https://static.example/3A204.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "3A204：三教 2" || content.Attachment == nil || content.Attachment.URL != "https://static.example/3A204.png" {
		t.Fatalf("content = %#v", content)
	}
}
