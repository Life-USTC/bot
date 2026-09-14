package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestSessionReadsGraphqlResourcesAndPrompt(t *testing.T) {
	serverMCP := mcpserver.NewMCPServer("context-server", "1.0.0", mcpserver.WithPaginationLimit(1))
	for _, uri := range []string{"life-ustc://graphql/schema", "life-ustc://graphql/operations"} {
		serverMCP.AddResource(mcpgo.Resource{URI: uri, Name: uri, MIMEType: "text/plain"},
			func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
				return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: req.Params.URI, MIMEType: "text/plain", Text: "type Query { catalog: Catalog! }"}}, nil
			})
	}
	serverMCP.AddPrompt(mcpgo.Prompt{Name: "plan_graphql_operation", Arguments: []mcpgo.PromptArgument{{Name: "goal", Required: true}}},
		func(_ context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
			if req.Params.Arguments["goal"] != "查看日历" {
				t.Errorf("prompt arguments = %#v", req.Params.Arguments)
			}
			return &mcpgo.GetPromptResult{Messages: []mcpgo.PromptMessage{{Role: mcpgo.RoleUser, Content: mcpgo.NewTextContent("Use graphql_operation_run for 查看日历")}}}, nil
		})
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(serverMCP))
	defer server.Close()
	session, err := New(server.URL, server.Client()).OpenSession(t.Context(), "token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	resources, err := session.Resources(t.Context())
	if err != nil || !json.Valid([]byte(resources)) || !strings.Contains(resources, "life-ustc://graphql/schema") || !strings.Contains(resources, "life-ustc://graphql/operations") {
		t.Fatalf("resources = %s, err=%v", resources, err)
	}
	contents, err := session.ReadResource(t.Context(), "life-ustc://graphql/schema")
	if err != nil || !strings.Contains(contents, "type Query") || !strings.Contains(contents, "text/plain") {
		t.Fatalf("contents = %s, err=%v", contents, err)
	}
	prompts, err := session.Prompts(t.Context())
	if err != nil || !strings.Contains(prompts, "plan_graphql_operation") || !strings.Contains(prompts, `"required":true`) {
		t.Fatalf("prompts = %s, err=%v", prompts, err)
	}
	prompt, err := session.GetPrompt(t.Context(), "plan_graphql_operation", map[string]string{"goal": "查看日历"})
	if err != nil || !strings.Contains(prompt, "graphql_operation_run") || !strings.Contains(prompt, "查看日历") {
		t.Fatalf("prompt = %s, err=%v", prompt, err)
	}
}
