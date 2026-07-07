package mcp

import (
	"context"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestToEinoToolsConvertsMetadataAndInvocation(t *testing.T) {
	mcpTools := []mcpgo.Tool{
		mcpgo.NewTool(
			"list_my_homeworks",
			mcpgo.WithDescription("List my homeworks."),
			mcpgo.WithBoolean("completed", mcpgo.Description("Completion filter.")),
		),
	}

	var gotName string
	var gotArgs map[string]any
	einoTools, err := ToEinoTools(mcpTools, func(_ context.Context, name string, args map[string]any) (string, error) {
		gotName = name
		gotArgs = args
		return `{"homeworks":[]}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(einoTools) != 1 {
		t.Fatalf("tool count = %d", len(einoTools))
	}

	info, err := einoTools[0].Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "list_my_homeworks" || info.Desc != "List my homeworks." {
		t.Fatalf("info = %#v", info)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("tool params schema is nil")
	}

	invokable, ok := einoTools[0].(einotool.InvokableTool)
	if !ok {
		t.Fatalf("tool does not implement InvokableTool: %T", einoTools[0])
	}
	out, err := invokable.InvokableRun(context.Background(), `{"completed":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"homeworks":[]}` {
		t.Fatalf("output = %q", out)
	}
	if gotName != "list_my_homeworks" {
		t.Fatalf("invoked name = %q", gotName)
	}
	if completed, ok := gotArgs["completed"].(bool); !ok || completed {
		t.Fatalf("args = %#v", gotArgs)
	}
}
