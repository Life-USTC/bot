package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
)

var errRepeatedToolCall = errors.New("agent repeated an identical tool call")

// toolRepeatGuard stops an agent from executing the same logical call twice in
// one model turn. It intentionally does not cache results: some tools mutate
// state or send messages, and replaying a cached success would misrepresent
// what happened. A new user follow-up resets the guard.
type toolRepeatGuard struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newToolRepeatGuard() *toolRepeatGuard {
	return &toolRepeatGuard{seen: make(map[string]struct{})}
}

func (g *toolRepeatGuard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.seen)
}

func (g *toolRepeatGuard) invokableMiddleware(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		if g.repeated(input) {
			return nil, repeatedToolCallError(input)
		}
		return next(ctx, input)
	}
}

func (g *toolRepeatGuard) streamableMiddleware(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		if g.repeated(input) {
			return nil, repeatedToolCallError(input)
		}
		return next(ctx, input)
	}
}

func (g *toolRepeatGuard) repeated(input *compose.ToolInput) bool {
	key := toolCallKey(input)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[key]; ok {
		return true
	}
	g.seen[key] = struct{}{}
	return false
}

func toolCallKey(input *compose.ToolInput) string {
	if input == nil {
		return "\x00"
	}
	return strings.TrimSpace(input.Name) + "\x00" + canonicalToolArguments(input.Arguments)
}

func canonicalToolArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return arguments
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return arguments
	}
	return string(canonical)
}

func repeatedToolCallError(input *compose.ToolInput) error {
	name := "unknown"
	if input != nil && strings.TrimSpace(input.Name) != "" {
		name = strings.TrimSpace(input.Name)
	}
	return errors.Join(errRepeatedToolCall, errors.New("tool: "+name))
}
