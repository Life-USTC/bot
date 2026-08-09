package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/compose"
)

func TestToolRepeatGuardCanonicalizesArgumentsAndDoesNotRepeatSideEffects(t *testing.T) {
	guard := newToolRepeatGuard()
	calls := 0
	next := func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		calls++
		return &compose.ToolOutput{Result: "sent"}, nil
	}
	endpoint := guard.invokableMiddleware(next)

	first := &compose.ToolInput{Name: "send_message_part", Arguments: `{"content":"hello","meta":{"b":2,"a":1}}`}
	if _, err := endpoint(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := &compose.ToolInput{Name: "send_message_part", Arguments: `{ "meta": {"a":1,"b":2}, "content":"hello" }`}
	if _, err := endpoint(context.Background(), second); !errors.Is(err, errRepeatedToolCall) {
		t.Fatalf("repeat error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("side-effect calls = %d, want 1", calls)
	}
}

func TestToolRepeatGuardResetAllowsNewFollowUpRound(t *testing.T) {
	guard := newToolRepeatGuard()
	calls := 0
	endpoint := guard.invokableMiddleware(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		calls++
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	input := &compose.ToolInput{Name: "get_current_time", Arguments: "{}"}
	if _, err := endpoint(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	guard.Reset()
	if _, err := endpoint(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls after reset = %d, want 2", calls)
	}
}
