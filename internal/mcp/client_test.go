package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
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
	var initializeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var rpcRequest struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &rpcRequest) == nil && rpcRequest.Method == "initialize" {
			mu.Lock()
			initializeRequests++
			mu.Unlock()
		}
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
	session, err := client.OpenSession(context.Background(), "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	tools, err := session.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "test_tool" {
		t.Fatalf("tools = %#v", tools)
	}

	result, err := session.Call(context.Background(), "test_tool", map[string]any{"input": "value"})
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
	if initializeRequests != 1 {
		t.Fatalf("initialize requests = %d, want 1", initializeRequests)
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

func TestSessionCallClassifiesMCPErrorResultAsRecoverable(t *testing.T) {
	mcpServer := mcpserver.NewMCPServer("test-server", "1.0.0")
	mcpServer.AddTool(
		mcpgo.NewTool("failing_tool"),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("write did not happen"), nil
		},
	)
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	defer server.Close()

	session, err := New(server.URL, server.Client()).OpenSession(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.Call(context.Background(), "failing_tool", nil)
	if err == nil || result != "" || !strings.Contains(err.Error(), "write did not happen") {
		t.Fatalf("result = %q, err = %v", result, err)
	}
	modelResult, ok := ModelToolErrorResult(err)
	if !ok || modelResult != "write did not happen" {
		t.Fatalf("modelResult = %q, ok = %v", modelResult, ok)
	}
}

func TestSessionCallRedactsMCPErrorDetails(t *testing.T) {
	mcpServer := mcpserver.NewMCPServer("test-server", "1.0.0")
	mcpServer.AddTool(
		mcpgo.NewTool("failing_tool"),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("authorization=private-value Bearer abc.def token=also-private invalid semester"), nil
		},
	)
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	defer server.Close()

	session, err := New(server.URL, server.Client()).OpenSession(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	_, err = session.Call(context.Background(), "failing_tool", nil)
	modelResult, ok := ModelToolErrorResult(err)
	if !ok || !strings.Contains(modelResult, "invalid semester") || !strings.Contains(modelResult, "[REDACTED]") {
		t.Fatalf("modelResult = %q, ok = %v", modelResult, ok)
	}
	for _, secret := range []string{"private-value", "abc.def", "also-private"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(modelResult, secret) {
			t.Fatalf("secret %q leaked: err=%q modelResult=%q", secret, err, modelResult)
		}
	}
}

func TestAuthorizationRequiredClassification(t *testing.T) {
	for _, err := range []error{
		transport.ErrAuthorizationRequired,
		fmt.Errorf("wrapped: %w", transport.ErrOAuthAuthorizationRequired),
	} {
		if !IsAuthorizationRequired(err) {
			t.Fatalf("error not classified: %v", err)
		}
	}
	if IsAuthorizationRequired(errors.New("server unavailable")) {
		t.Fatal("ordinary failure classified as authorization required")
	}
}

func TestClientDiscoversEveryToolAcrossPagesWithServerMetadata(t *testing.T) {
	mcpServer := mcpserver.NewMCPServer("paged-server", "1.0.0", mcpserver.WithPaginationLimit(1))
	for _, candidate := range []mcpgo.Tool{
		mcpgo.NewTool("catalog_first", mcpgo.WithReadOnlyHintAnnotation(true)),
		mcpgo.NewTool("workspace_new_feature", mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(false),
			mcpgo.WithString("kind", mcpgo.Required(), mcpgo.Enum("regular", "auditor"))),
		mcpgo.NewTool("workspace_remove", mcpgo.WithReadOnlyHintAnnotation(false), mcpgo.WithDestructiveHintAnnotation(true)),
	} {
		mcpServer.AddTool(candidate, func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		})
	}
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	defer server.Close()
	session, err := New(server.URL, server.Client()).OpenSession(t.Context(), "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %#v, want all three pages", tools)
	}
	var writeTool *mcpgo.Tool
	for i := range tools {
		if tools[i].Name == "workspace_new_feature" {
			writeTool = &tools[i]
		}
	}
	if writeTool == nil || writeTool.Annotations.ReadOnlyHint == nil || *writeTool.Annotations.ReadOnlyHint ||
		writeTool.Annotations.DestructiveHint == nil || *writeTool.Annotations.DestructiveHint {
		t.Fatalf("write annotations lost: %#v", writeTool)
	}
	encoded, err := toolInputSchemaBytes(*writeTool)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"enum"`, `"regular"`, `"auditor"`, `"required":["kind"]`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Fatalf("server schema missing %s: %s", want, encoded)
		}
	}
}
