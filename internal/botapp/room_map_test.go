package botapp

import (
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
)

func TestRoomMapPresentationPreservesTextAndImage(t *testing.T) {
	content, err := (&Coordinator{}).presentationContent(t.Context(), commands.Response{
		Text:  "3A204：三教 2",
		Image: &responses.Image{Kind: "room-map", URL: "https://static.example/3A204.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Parts) != 2 || content.TextContent() != "3A204：三教 2" || content.Parts[1].Attachment == nil || content.Parts[1].Attachment.URL != "https://static.example/3A204.png" {
		t.Fatalf("content = %#v", content)
	}
}
