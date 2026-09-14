package botapp

import (
	"context"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
)

func TestCoordinatorDoesNotImposeExecutionDeadline(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "no deadline", true: "caller deadline"}[callerDeadline], func(t *testing.T) {
			db := newCoordinatorStore(t)
			ctx := t.Context()
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Hour)
				defer cancel()
			}
			expected, hasDeadline := ctx.Deadline()
			called := false
			coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Outputs: db,
				Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) { return commands.Response{}, false }),
				Agent: agentFunc(func(runCtx context.Context, _ agent.Input) (commands.Response, bool) {
					called = true
					actual, has := runCtx.Deadline()
					if has != hasDeadline || has && !actual.Equal(expected) {
						t.Fatalf("coordinator imposed a deadline: got %v want %v", actual, expected)
					}
					return commands.Response{Text: "任务完成"}, true
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Enqueue(ctx, jobInbound("long-task", "帮我继续处理")); err != nil {
				t.Fatal(err)
			}
			job := claimOnlyConversationJob(t, db)
			originalClaim := time.Now().Add(-10 * time.Minute)
			job.ClaimedAt = &originalClaim
			coordinator.execute(ctx, job)
			if !called {
				t.Fatal("old claim timestamp prevented live task execution")
			}
		})
	}
}
