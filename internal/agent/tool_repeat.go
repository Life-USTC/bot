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

const toolFailureRepeatLimit = 1

// toolRepeatGuard stops an agent from executing the same logical call twice in
// one model turn and stops a failed logical plan from being retried in a later
// follow-up round. It intentionally does not cache results: some tools mutate
// state or send messages, and replaying a cached success would misrepresent
// what happened. Reset only clears the current-turn duplicate guard; failure
// history remains for the lifetime of this run.
type toolRepeatGuard struct {
	mu       sync.Mutex
	seen     map[string]struct{}
	failures map[string]int
}

func newToolRepeatGuard() *toolRepeatGuard {
	return &toolRepeatGuard{
		seen:     make(map[string]struct{}),
		failures: make(map[string]int),
	}
}

func (g *toolRepeatGuard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.seen)
}

func (g *toolRepeatGuard) invokableMiddleware(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		if err := g.admit(input); err != nil {
			return nil, err
		}
		out, err := next(ctx, input)
		g.recordResult(ctx, input, err)
		return out, err
	}
}

func (g *toolRepeatGuard) streamableMiddleware(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		if err := g.admit(input); err != nil {
			return nil, err
		}
		out, err := next(ctx, input)
		g.recordStreamResult(input, out, err)
		return out, err
	}
}

func (g *toolRepeatGuard) admit(input *compose.ToolInput) error {
	key := toolCallKey(input)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[key]; ok {
		return repeatedToolCallError(input)
	}
	if g.failures[key] >= toolFailureRepeatLimit {
		return nonProgressingToolPlanError(input)
	}
	g.seen[key] = struct{}{}
	return nil
}

func (g *toolRepeatGuard) recordResult(ctx context.Context, input *compose.ToolInput, err error) {
	key := toolCallKey(input)
	if !toolResultFailed(ctx, input, err) {
		g.mu.Lock()
		delete(g.failures, key)
		g.mu.Unlock()
		return
	}
	g.recordFailure(input)
}

func (g *toolRepeatGuard) recordStreamResult(input *compose.ToolInput, output *compose.StreamToolOutput, err error) {
	// Stream readers are intentionally not consumed by the guard. MCP tools use
	// the invokable endpoint; transport/cancellation errors still count as a
	// failed plan for stream-only tools.
	if err != nil {
		g.recordFailure(input)
	}
}

func (g *toolRepeatGuard) recordFailure(input *compose.ToolInput) {
	key := toolCallKey(input)
	g.mu.Lock()
	g.failures[key]++
	g.mu.Unlock()
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

func nonProgressingToolPlanError(input *compose.ToolInput) error {
	name := "unknown"
	if input != nil && strings.TrimSpace(input.Name) != "" {
		name = strings.TrimSpace(input.Name)
	}
	return errors.Join(errAgentNonProgress, errors.New("tool: "+name))
}

func toolResultFailed(ctx context.Context, input *compose.ToolInput, err error) bool {
	if err != nil {
		return !errors.Is(err, errAgentToolCallBudget) &&
			!errors.Is(err, errAgentRunDeadline) &&
			!errors.Is(err, errAgentContextBudget) &&
			!errors.Is(err, context.Canceled)
	}
	if input == nil {
		return false
	}
	return toolOutcomesFromContext(ctx).isError(input.CallID)
}
