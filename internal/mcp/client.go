package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// Client creates short-lived authenticated MCP sessions. Tokens are per-user,
// so each public method accepts a token instead of storing one on the client.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}
}

func (c *Client) newSession(token string) (*mcpclient.Client, error) {
	headers := map[string]string{}
	if token = strings.TrimSpace(token); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return mcpclient.NewStreamableHttpClient(
		c.baseURL,
		transport.WithHTTPBasicClient(c.httpClient),
		transport.WithHTTPHeaders(headers),
	)
}

func (c *Client) initialize(ctx context.Context, token string) (*mcpclient.Client, error) {
	session, err := c.newSession(token)
	if err != nil {
		return nil, fmt.Errorf("create mcp client: %w", err)
	}
	if err := session.Start(ctx); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("start mcp client: %w", err)
	}
	req := mcpgo.InitializeRequest{}
	req.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcpgo.Implementation{
		Name:    "life-ustc-bot",
		Version: "1.0.0",
	}
	if _, err := session.Initialize(ctx, req); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("initialize mcp client: %w", err)
	}
	return session, nil
}

func (c *Client) Tools(ctx context.Context, token string) ([]mcpgo.Tool, error) {
	session, err := c.initialize(ctx, token)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()

	result, err := session.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}
	return result.Tools, nil
}

func (c *Client) Call(ctx context.Context, token, name string, arguments map[string]any) (string, error) {
	session, err := c.initialize(ctx, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close() }()

	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments
	result, err := session.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("call mcp tool %s: %w", name, err)
	}
	return textFromToolResult(result), nil
}

func textFromToolResult(result *mcpgo.CallToolResult) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	for _, content := range result.Content {
		switch typed := content.(type) {
		case mcpgo.TextContent:
			b.WriteString(typed.Text)
		case *mcpgo.TextContent:
			b.WriteString(typed.Text)
		default:
			if data, err := json.Marshal(content); err == nil {
				b.Write(data)
			}
		}
	}
	if b.Len() == 0 && result.StructuredContent != nil {
		if data, err := json.Marshal(result.StructuredContent); err == nil {
			b.Write(data)
		}
	}
	return b.String()
}
