package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

var errRepeatedToolCall = errors.New("agent repeated an identical tool call")

const toolFailureRepeatLimit = 1

// maxRepeatRefusals bounds how often one run may tell the model that its call
// was refused. A model that adapts needs one or two of these; a model stuck in
// a loop would otherwise keep paying for full-size requests until the agent
// framework's own iteration cap, so the run aborts once the budget is spent.
const maxRepeatRefusals = 3

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
	refusals int
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
			// A rejected repeat is the model's planning problem, not a run
			// failure. Returning it as a tool error lets the model pick a
			// different plan; returning a Go error would abort the whole turn
			// and replace a usable answer with "AI 助手出错". A model that keeps
			// repeating still hits the refusal budget and ends the run.
			if !g.admitRefusal() {
				return nil, err
			}
			toolOutcomesFromContext(ctx).markError(input.CallID)
			return &compose.ToolOutput{Result: repeatGuardToolResult(input, err)}, nil
		}
		out, err := next(ctx, input)
		g.recordResult(ctx, input, err)
		return out, err
	}
}

func (g *toolRepeatGuard) streamableMiddleware(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		if err := g.admit(input); err != nil {
			if !g.admitRefusal() {
				return nil, err
			}
			toolOutcomesFromContext(ctx).markError(input.CallID)
			return &compose.StreamToolOutput{
				Result: schema.StreamReaderFromArray([]string{repeatGuardToolResult(input, err)}),
			}, nil
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

// admitRefusal reports whether this run may still spend a model round trip on
// telling the model that its call was refused.
func (g *toolRepeatGuard) admitRefusal() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refusals >= maxRepeatRefusals {
		return false
	}
	g.refusals++
	return true
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

// repeatGuardToolResult reports the refusal in the tool channel, in the same
// machine-readable shape as any other host result.
func repeatGuardToolResult(input *compose.ToolInput, err error) string {
	rejection := hostToolRejection{Outcome: toolOutcomeRejected}
	if input != nil {
		rejection.Tool = strings.TrimSpace(input.Name)
	}
	switch {
	case errors.Is(err, errRepeatedToolCall):
		rejection.Detail = "an identical call already ran in this turn; the host did not repeat it"
	case errors.Is(err, errAgentNonProgress):
		rejection.Detail = "an identical call already failed in this turn; the host did not retry it"
	default:
		rejection.Detail = err.Error()
	}
	return encodeToolResult(rejection)
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
			!errors.Is(err, errAgentRunTokenBudget) &&
			!errors.Is(err, context.Canceled)
	}
	if input == nil {
		return false
	}
	return toolOutcomesFromContext(ctx).isError(input.CallID)
}
