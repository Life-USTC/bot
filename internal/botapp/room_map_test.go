package botapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/responses"
)

func TestRoomMapPresentationOnlyContainsImage(t *testing.T) {
	content, err := (&Coordinator{}).presentationContent(t.Context(), commands.RoomMapResponse(life.RoomMap{
		Code: "5201", Building: "五教", Floor: "2", Status: "highlighted",
		ImageURL: "https://static.example/5201.png",
	}))

	if err != nil {
		t.Fatal(err)
	}
	if len(content.Parts) != 1 || content.TextContent() != "" || content.Parts[0].Attachment == nil || content.Parts[0].Attachment.URL != "https://static.example/5201.png" {
		t.Fatalf("content = %#v", content)
	}
}

func TestRoomMapPresentationFailureDoesNotFallBackToText(t *testing.T) {
	content, err := (&Coordinator{}).presentationContent(t.Context(), commands.Response{
		Image: &responses.Image{Kind: "room-map", AltText: "5201：五教 2"},
	})
	if err == nil || len(content.Parts) != 0 {
		t.Fatalf("content = %#v, err = %v", content, err)
	}
}

func TestGroupRoom5201OutboxOnlyImageAndStructuredLLMResult(t *testing.T) {
	room := life.RoomMap{Code: "5201", Building: "五教", Floor: "2", Status: "highlighted", ImageURL: "https://static.example/5201.png"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/rooms/5201/map" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(room)
	}))
	defer server.Close()
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db, Commands: commands.Handler{Life: life.NewClient(server.URL, server.Client())}, Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := groupJobInbound("room-5201", "42", "5201")
	inbound.BotMentioned = true
	if err := coordinator.Enqueue(t.Context(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)
	records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox = %#v", records)
	}
	content := records[0].Message.Content
	if len(content.Parts) != 1 || content.TextContent() != "" || content.Parts[0].Attachment == nil || content.Parts[0].Attachment.URL != room.ImageURL {
		t.Fatalf("outbox content = %#v", content)
	}
	executions, err := db.CapabilityExecutionsForJob(t.Context(), job.ID)
	if err != nil || len(executions) != 1 {
		t.Fatalf("executions = %#v, err = %v", executions, err)
	}
	var result struct {
		Result struct {
			Room life.RoomMap `json:"room"`
		} `json:"data"`
		Images []struct {
			ID string `json:"id"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(executions[0].Result), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result.Room.Code != "5201" || result.Result.Room.Floor != "2" || len(result.Images) != 1 || result.Images[0].ID == "" {
		t.Fatalf("LLM result = %s", executions[0].Result)
	}
}
