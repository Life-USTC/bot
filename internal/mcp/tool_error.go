package mcp

import (
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

// NewRecoverableToolError lets fixed host meta-tools report a correctable
// input/tool-selection failure through the same sanitized result path as MCP.
func NewRecoverableToolError(tool, detail string) error {
	return newToolExecutionError(tool, detail)
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

// ModelToolErrorResult converts only recoverable tool execution errors into
// their sanitized domain error. It deliberately adds no success/status wrapper
// or model instruction around what the tool actually reported.
func ModelToolErrorResult(err error) (string, bool) {
	var toolErr *toolExecutionError
	if !errors.As(err, &toolErr) || toolErr == nil {
		return "", false
	}
	return toolErr.detail, true
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
