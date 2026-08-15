package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const maxToolErrorRunes = 500

var (
	bearerCredentialPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	namedCredentialPattern  = regexp.MustCompile(`(?i)((?:access[_-]?token|refresh[_-]?token|token|device[_-]?code|user[_-]?code|client[_-]?secret|secret|authorization|api[_-]?key)[[:space:]]*["']?[[:space:]]*[:=][[:space:]]*["']?)[^"'[:space:],;&]+`)
)

// toolExecutionError is an MCP tool-level failure that the model can usually
// recover from by correcting arguments or choosing another tool. Transport,
// authorization, cancellation, and timeout errors intentionally use their
// original error types so the host can handle them deterministically.
type toolExecutionError struct {
	tool   string
	detail string
}

func newToolExecutionError(tool, detail string) error {
	detail = sanitizeToolErrorText(detail)
	if detail == "" {
		detail = "工具没有完成请求。"
	}
	return &toolExecutionError{tool: strings.TrimSpace(tool), detail: detail}
}

func (e *toolExecutionError) Error() string {
	if e == nil {
		return "mcp tool execution failed"
	}
	if e.tool == "" {
		return "mcp tool execution failed: " + e.detail
	}
	return fmt.Sprintf("mcp tool %s failed: %s", e.tool, e.detail)
}

// ModelToolErrorResult converts only recoverable tool execution errors into a
// structured tool result. Other errors must continue up to the host.
func ModelToolErrorResult(err error) (string, bool) {
	var toolErr *toolExecutionError
	if !errors.As(err, &toolErr) || toolErr == nil {
		return "", false
	}
	payload := struct {
		OK    bool `json:"ok"`
		Error struct {
			Type    string `json:"type"`
			Tool    string `json:"tool,omitempty"`
			Message string `json:"message"`
		} `json:"error"`
		Instruction string `json:"instruction"`
	}{
		OK:          false,
		Instruction: "根据错误修正工具参数后再试；如果无法修正，请用简短文字向用户说明，不要复述内部错误，也不要生成图片指令。",
	}
	payload.Error.Type = "tool_execution_failed"
	payload.Error.Tool = toolErr.tool
	payload.Error.Message = toolErr.detail
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return `{"ok":false,"error":{"type":"tool_execution_failed","message":"工具没有完成请求。"}}`, true
	}
	return string(data), true
}

func sanitizeToolErrorText(text string) string {
	text = strings.TrimSpace(text)
	text = bearerCredentialPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = namedCredentialPattern.ReplaceAllString(text, "${1}[REDACTED]")
	runes := []rune(text)
	if len(runes) > maxToolErrorRunes {
		return string(runes[:maxToolErrorRunes]) + "..."
	}
	return text
}
