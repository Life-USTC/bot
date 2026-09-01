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

	first := &compose.ToolInput{Name: "side_effect", Arguments: `{"content":"hello","meta":{"b":2,"a":1}}`}
	if _, err := endpoint(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := &compose.ToolInput{Name: "side_effect", Arguments: `{ "meta": {"a":1,"b":2}, "content":"hello" }`}
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

func TestToolRepeatGuardStopsEquivalentFailingPlanAcrossRounds(t *testing.T) {
	guard := newToolRepeatGuard()
	ctx := withToolOutcomes(context.Background(), newToolOutcomeRegistry())
	endpoint := guard.invokableMiddleware(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		toolOutcomesFromContext(ctx).markError(input.CallID)
		return &compose.ToolOutput{Result: "参数无效"}, nil
	})
	first := &compose.ToolInput{Name: "search_courses", CallID: "call-1", Arguments: `{"query":"数学分析","limit":10}`}
	if _, err := endpoint(ctx, first); err != nil {
		t.Fatal(err)
	}
	guard.Reset()
	second := &compose.ToolInput{Name: "search_courses", CallID: "call-2", Arguments: `{ "limit": 10, "query": "数学分析" }`}
	if _, err := endpoint(ctx, second); !errors.Is(err, errAgentNonProgress) {
		t.Fatalf("equivalent failing plan error = %v", err)
	}
}

func TestToolResultFailureUsesHostStateNotModelText(t *testing.T) {
	ctx := withToolOutcomes(context.Background(), newToolOutcomeRegistry())
	failed := &compose.ToolInput{CallID: "failed"}
	toolOutcomesFromContext(ctx).markError(failed.CallID)
	if !toolResultFailed(ctx, failed, nil) {
		t.Fatal("typed tool error state was ignored")
	}
	if toolResultFailed(ctx, &compose.ToolInput{CallID: "success"}, nil) {
		t.Fatal("ordinary literal result was classified as failure")
	}
}

func TestToolRepeatGuardAllowsChangedSuccessfulPlan(t *testing.T) {
	guard := newToolRepeatGuard()
	endpoint := guard.invokableMiddleware(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "查询完成"}, nil
	})
	input := &compose.ToolInput{Name: "search_courses", Arguments: `{"query":"数学分析"}`}
	if _, err := endpoint(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	guard.Reset()
	if _, err := endpoint(context.Background(), input); err != nil {
		t.Fatalf("successful plan after reset = %v", err)
	}
}
