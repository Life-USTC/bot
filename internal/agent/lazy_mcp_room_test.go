package agent

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestRoomMapMCPIsAllowedAndDeliversImageResponse(t *testing.T) {
	var delivered commands.Response
	lazy := &lazyMCPSession{
		service:  &Service{handler: commands.Handler{}},
		identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "private", ConversationID: "7"},
		sendResponse: func(_ context.Context, _ store.Identity, response commands.Response) error {
			delivered = response
			return nil
		},
	}
	if err := lazy.deliverRoomMapResponse(t.Context(), `{"code":"3A204","building":"三教","floor":"2","status":"highlighted","imageUrl":"https://static.example/3A204.png","sourceImageUrl":"https://static.example/floor-2.png"}`); err != nil {
		t.Fatal(err)
	}
	if delivered.Text != "" || delivered.Image == nil || delivered.Image.URL != "https://static.example/3A204.png" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

func TestCampusToolEffectUsesMCPAnnotations(t *testing.T) {
	boolPtr := func(value bool) *bool { return &value }
	cases := []struct {
		name string
		tool mcpgo.Tool
		want campusToolEffect
	}{
		{name: "read", tool: mcpgo.Tool{Annotations: mcpgo.ToolAnnotation{ReadOnlyHint: boolPtr(true), DestructiveHint: boolPtr(true)}}, want: campusEffectRead},
		{name: "write", tool: mcpgo.Tool{Annotations: mcpgo.ToolAnnotation{ReadOnlyHint: boolPtr(false), DestructiveHint: boolPtr(false)}}, want: campusEffectWrite},
		{name: "destructive", tool: mcpgo.Tool{Annotations: mcpgo.ToolAnnotation{ReadOnlyHint: boolPtr(false), DestructiveHint: boolPtr(true)}}, want: campusEffectDestructive},
		{name: "unannotated", tool: mcpgo.Tool{}, want: campusEffectDestructive},
	}
	for _, tc := range cases {
		if got := campusEffectOf(tc.tool); got != tc.want {
			t.Errorf("%s effect = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGraphQLConfirmedArgumentIsHostControlled(t *testing.T) {
	modelArguments := map[string]any{
		"operation": "mutation { updateHomework }",
		"confirmed": true,
	}
	pending := campusArgumentsForRemote("graphql_operation_run", modelArguments, false)
	if pending["confirmed"] != false {
		t.Fatalf("pending GraphQL arguments = %#v, want confirmed=false", pending)
	}
	if modelArguments["confirmed"] != true {
		t.Fatalf("model arguments were mutated = %#v", modelArguments)
	}
	approved := campusArgumentsForRemote("graphql_operation_run", pending, true)
	if approved["confirmed"] != true {
		t.Fatalf("approved GraphQL arguments = %#v, want confirmed=true", approved)
	}
}

func TestRoomMapMCPDeliversOnlyImageWhenRenderedImagesAreDisabled(t *testing.T) {
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
	if delivered.Text != "" || delivered.Image == nil || delivered.Image.URL != "https://static.example/3A204.png" {
		t.Fatalf("delivered = %#v", delivered)
	}
}
