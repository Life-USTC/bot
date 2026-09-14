package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestRoomMapMCPIsAllowedAndDeliversImageResponse(t *testing.T) {
	if !campusReadToolAllowed("catalog_rooms_map") {
		t.Fatal("catalog_rooms_map is not in the read-only allowlist")
	}
	var delivered commands.Response
	lazy := &lazyMCPSession{
		service:  &Service{handler: commands.Handler{EnableImageResponses: true}},
		identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "private", ConversationID: "7"},
		sendResponse: func(_ context.Context, _ store.Identity, response commands.Response) error {
			delivered = response
			return nil
		},
	}
	if err := lazy.deliverRoomMapResponse(t.Context(), `{"code":"3A204","building":"三教","floor":"2","status":"highlighted","imageUrl":"https://static.example/3A204.png","sourceImageUrl":"https://static.example/floor-2.png"}`); err != nil {
		t.Fatal(err)
	}
	if delivered.Text != "3A204：三教 2" || delivered.Image == nil || delivered.Image.URL != "https://static.example/3A204.png" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

func TestCampusReadToolAllowlistContainsOnlyCurrentReadTools(t *testing.T) {
	for _, name := range []string{"catalog_young_event_list", "catalog_young_event_get", "catalog_rooms_map"} {
		if !campusReadToolAllowed(name) {
			t.Errorf("current read tool %q is not in the host allowlist", name)
		}
	}
	for _, name := range []string{
		"get_current_semester", "list_my_homeworks", "search_courses",
		"delete_my_homework", "workspace_subscription_kind_update", "arbitrary_tool",
	} {
		if campusReadToolAllowed(name) {
			t.Errorf("disallowed MCP tool %q is in the host allowlist", name)
		}
	}
}

func TestRoomMapMCPKeepsURLWhenRenderedImagesAreDisabled(t *testing.T) {
	var delivered commands.Response
	lazy := &lazyMCPSession{
		service:  &Service{handler: commands.Handler{}},
		identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "private", ConversationID: "7"},
		sendResponse: func(_ context.Context, _ store.Identity, response commands.Response) error {
			delivered = response
			return nil
		},
	}
	if err := lazy.deliverRoomMapResponse(t.Context(), `{"code":"3A204","building":"三教","floor":"2","status":"highlighted","imageUrl":"https://static.example/3A204.png"}`); err != nil {
		t.Fatal(err)
	}
	if delivered.Image != nil || !strings.Contains(delivered.Text, "地图：https://static.example/3A204.png") {
		t.Fatalf("delivered = %#v", delivered)
	}
}
