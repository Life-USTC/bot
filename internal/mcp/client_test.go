package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestClientListsAndCallsToolsWithBearerToken(t *testing.T) {
	mcpServer := mcpserver.NewMCPServer("test-server", "1.0.0")
	mcpServer.AddTool(
		mcpgo.NewTool(
			"test_tool",
			mcpgo.WithDescription("A test tool."),
			mcpgo.WithString("input", mcpgo.Description("Input text.")),
		),
		func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok:" + req.GetString("input", "")), nil
		},
	)
	handler := mcpserver.NewStreamableHTTPServer(mcpServer)

	var mu sync.Mutex
	var requests []struct {
		method string
		auth   string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, struct {
			method string
			auth   string
		}{method: r.Method, auth: r.Header.Get("Authorization")})
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	client := New(server.URL, server.Client())
	tools, err := client.Tools(context.Background(), "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "test_tool" {
		t.Fatalf("tools = %#v", tools)
	}

	result, err := client.Call(context.Background(), "test-token", "test_tool", map[string]any{"input": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "ok:value" {
		t.Fatalf("result = %q", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no MCP HTTP requests observed")
	}
	for _, req := range requests {
		if req.method == http.MethodPost && req.auth != "Bearer test-token" {
			t.Fatalf("requests = %#v", requests)
		}
	}
}

func TestToolResultTextJoinsTextContent(t *testing.T) {
	result := textFromToolResult(&mcpgo.CallToolResult{
		Content: []mcpgo.Content{
			mcpgo.TextContent{Type: mcpgo.ContentTypeText, Text: "first"},
			mcpgo.TextContent{Type: mcpgo.ContentTypeText, Text: " second"},
		},
	})

	if strings.TrimSpace(result) != "first second" {
		t.Fatalf("result = %q", result)
	}
}
