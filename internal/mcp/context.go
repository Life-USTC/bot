package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// Resources lists the server's resources and URI templates, including all pages.
func (s *Session) Resources(ctx context.Context) (string, error) {
	resources, err := s.client.ListResources(ctx, mcpgo.ListResourcesRequest{})
	if err != nil {
		return "", fmt.Errorf("list mcp resources: %w", err)
	}
	templates, err := s.client.ListResourceTemplates(ctx, mcpgo.ListResourceTemplatesRequest{})
	if err != nil {
		return "", fmt.Errorf("list mcp resource templates: %w", err)
	}
	data, err := json.Marshal(struct {
		Resources []mcpgo.Resource         `json:"resources"`
		Templates []mcpgo.ResourceTemplate `json:"resourceTemplates"`
	}{Resources: resources.Resources, Templates: templates.ResourceTemplates})
	return string(data), err
}

func (s *Session) ReadResource(ctx context.Context, uri string) (string, error) {
	request := mcpgo.ReadResourceRequest{}
	request.Params.URI = uri
	result, err := s.client.ReadResource(ctx, request)
	if err != nil {
		return "", fmt.Errorf("read mcp resource: %w", err)
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func (s *Session) Prompts(ctx context.Context) (string, error) {
	result, err := s.client.ListPrompts(ctx, mcpgo.ListPromptsRequest{})
	if err != nil {
		return "", fmt.Errorf("list mcp prompts: %w", err)
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func (s *Session) GetPrompt(ctx context.Context, name string, arguments map[string]string) (string, error) {
	request := mcpgo.GetPromptRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := s.client.GetPrompt(ctx, request)
	if err != nil {
		return "", fmt.Errorf("get mcp prompt: %w", err)
	}
	data, err := json.Marshal(result)
	return string(data), err
}
