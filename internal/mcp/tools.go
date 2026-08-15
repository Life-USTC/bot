package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type InvokeFunc func(ctx context.Context, name string, arguments map[string]any) (string, error)

func ToEinoTools(mcpTools []mcpgo.Tool, invoke InvokeFunc) ([]einotool.BaseTool, error) {
	out := make([]einotool.BaseTool, 0, len(mcpTools))
	for _, mcpTool := range mcpTools {
		info, err := toolInfo(mcpTool)
		if err != nil {
			return nil, err
		}
		out = append(out, &einoMCPTool{
			info:   info,
			invoke: invoke,
		})
	}
	return out, nil
}

type einoMCPTool struct {
	info   *einoschema.ToolInfo
	invoke InvokeFunc
}

func (t *einoMCPTool) Info(context.Context) (*einoschema.ToolInfo, error) {
	return t.info, nil
}

func (t *einoMCPTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	var args map[string]any
	if argumentsInJSON != "" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", newToolExecutionError(t.info.Name, "工具参数不是有效的 JSON："+err.Error())
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	return t.invoke(ctx, t.info.Name, args)
}

func toolInfo(mcpTool mcpgo.Tool) (*einoschema.ToolInfo, error) {
	schemaBytes, err := toolInputSchemaBytes(mcpTool)
	if err != nil {
		return nil, err
	}
	inputSchema := &jsonschema.Schema{}
	if err := json.Unmarshal(schemaBytes, inputSchema); err != nil {
		return nil, fmt.Errorf("parse input schema for %s: %w", mcpTool.Name, err)
	}
	return &einoschema.ToolInfo{
		Name:        mcpTool.Name,
		Desc:        mcpTool.Description,
		ParamsOneOf: einoschema.NewParamsOneOfByJSONSchema(inputSchema),
	}, nil
}

func toolInputSchemaBytes(mcpTool mcpgo.Tool) ([]byte, error) {
	if len(mcpTool.RawInputSchema) > 0 {
		return mcpTool.RawInputSchema, nil
	}
	data, err := json.Marshal(mcpTool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("marshal input schema for %s: %w", mcpTool.Name, err)
	}
	return data, nil
}
