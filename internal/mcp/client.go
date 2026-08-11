package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// Client creates authenticated MCP sessions. Tokens are per-user, so callers
// open one session for each agent run and close it when the run finishes.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Session struct {
	client *mcpclient.Client
}

func IsAuthorizationRequired(err error) bool {
	return errors.Is(err, transport.ErrAuthorizationRequired) ||
		errors.Is(err, transport.ErrOAuthAuthorizationRequired)
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

func (c *Client) OpenSession(ctx context.Context, token string) (*Session, error) {
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
	return &Session{client: session}, nil
}

func (s *Session) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *Session) Tools(ctx context.Context) ([]mcpgo.Tool, error) {
	result, err := s.client.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}
	return result.Tools, nil
}

func (s *Session) Call(ctx context.Context, name string, arguments map[string]any) (string, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments
	result, err := s.client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("call mcp tool %s: %w", name, err)
	}
	text := textFromToolResult(result)
	if result != nil && result.IsError {
		return "", fmt.Errorf("mcp tool %s failed: %s", name, limitedErrorText(text))
	}
	return text, nil
}

func limitedErrorText(text string) string {
	const maxRunes = 500
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
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
