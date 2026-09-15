package botapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestCoordinatorDirectDestructiveCommandWaitsForConfirmationAndContinuesOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
		Text: "退出完成", Kind: "account", Data: map[string]any{"cleared": true},
	})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sentAt := time.Date(2026, 9, 14, 6, 7, 8, 0, time.UTC)
	inbound := jobInbound("direct-dangerous", "账户 退出")
	inbound.Actor.DisplayName = "张三"
	inbound.SentAt = sentAt
	inbound.ReceivedAt = sentAt.Add(time.Second)
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionAwaitingConfirmation {
		t.Fatalf("direct destructive execution=%#v err=%v", executions, err)
	}
	if handler.calls != 0 {
		t.Fatalf("destructive command ran before confirmation: calls=%d", handler.calls)
	}
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation {
		t.Fatalf("confirmation job=%#v err=%v", saved, err)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), confirmationPrompt) {
		t.Fatalf("confirmation output=%#v err=%v", records, err)
	}
	acceptCoordinatorOutputs(t, db, records)

	confirmation := jobInbound("direct-dangerous-confirm", "确认")
	confirmation.Actor.DisplayName = inbound.Actor.DisplayName
	if err := coordinator.Enqueue(ctx, confirmation); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("direct-dangerous-confirm-duplicate", "确认")); err != nil {
		t.Fatal(err)
	}
	resumed := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, resumed)
	if handler.calls != 1 {
		t.Fatalf("destructive command executed %d times, want once", handler.calls)
	}
	executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("resumed destructive execution=%#v err=%v", executions, err)
	}
	if records, err = db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "#退出（已完成）") {
		t.Fatalf("success output=%#v err=%v", records, err)
	}

	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("command events=%#v err=%v", events, err)
	}
	if events[0].Type != store.ConversationEventUser || events[0].Source != store.ConversationEventSourceCommand ||
		events[0].ActorDisplayName != "张三" || !events[0].OccurredAt.Equal(sentAt) {
		t.Fatalf("user command event=%#v", events[0])
	}
	if events[1].Type != store.ConversationEventAssistant || events[1].Source != store.ConversationEventSourceCommand {
		t.Fatalf("assistant command event=%#v", events[1])
	}
	var result struct {
		Source     string    `json:"source"`
		Operation  string    `json:"operation"`
		Status     string    `json:"status"`
		ObservedAt time.Time `json:"observed_at"`
		Result     struct {
			Cleared bool `json:"cleared"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(events[1].Content), &result); err != nil {
		t.Fatalf("assistant command content=%q err=%v", events[1].Content, err)
	}
	if result.Source != "bot" || result.Operation != "logout" || result.Status != "succeeded" || !result.Result.Cleared || result.ObservedAt.IsZero() {
		t.Fatalf("assistant command result=%#v", result)
	}
}

func TestCoordinatorDirectDestructiveCommandRejectsWithoutExecution(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Text: "不应执行", Kind: "account"})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inbound := jobInbound("direct-dangerous-reject", "账户 退出")
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	acceptCoordinatorOutputs(t, db, records)
	if err := coordinator.Enqueue(ctx, jobInbound("direct-dangerous-no", "拒绝")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	if handler.calls != 0 {
		t.Fatalf("rejected destructive command executed %d times", handler.calls)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionDenied {
		t.Fatalf("rejected execution=%#v err=%v", executions, err)
	}
	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil || len(events) != 2 || events[0].Type != store.ConversationEventUser || events[1].Type != store.ConversationEventAssistant {
		t.Fatalf("confirmation mechanic leaked into events=%#v err=%v", events, err)
	}
	records, err = db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "已拒绝") {
		t.Fatalf("rejection output=%#v err=%v", records, err)
	}
}

func TestCoordinatorDirectDestructiveBatchConfirmsEachOperation(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
		Text: "已删除", Kind: "todo", Data: map[string]any{"deleted": true},
	})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-delete-batch", "待办 删除 1,2")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 {
		t.Fatalf("prepared destructive batch=%#v err=%v", executions, err)
	}
	for index, execution := range executions {
		if execution.Sequence != index || execution.State != store.CapabilityExecutionAwaitingConfirmation || execution.Capability != string(commands.CapabilityTodo) ||
			len(execution.Arguments) != 2 || execution.Arguments[0] != "delete" || execution.Arguments[1] != string(rune('1'+index)) {
			t.Fatalf("prepared destructive execution[%d]=%#v", index, execution)
		}
		if execution.Receipt.Action != "删除" || execution.Receipt.Resource != "待办" || execution.Receipt.Subject != string(rune('1'+index)) {
			t.Fatalf("destructive receipt[%d]=%#v", index, execution.Receipt)
		}
	}
	if len(handler.described) != 2 || handler.calls != 0 {
		t.Fatalf("preflight/calls: described=%#v calls=%d", handler.described, handler.calls)
	}
	if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 ||
		!strings.Contains(records[0].Message.Content.TextContent(), "#待办 delete 1（待确认：1）") || strings.Contains(records[0].Message.Content.TextContent(), "#待办 delete 2（待确认：2）") {
		t.Fatalf("first confirmation output=%#v err=%v", records, err)
	} else {
		acceptCoordinatorOutputs(t, db, records)
	}

	if err := coordinator.Enqueue(ctx, jobInbound("direct-delete-batch-confirm-1", "确认")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	if handler.calls != 1 || len(handler.allArgs) != 1 || strings.Join(handler.allArgs[0], " ") != "delete 1" {
		t.Fatalf("first destructive execution: calls=%d args=%#v", handler.calls, handler.allArgs)
	}
	executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 || executions[0].State != store.CapabilityExecutionSucceeded || executions[1].State != store.CapabilityExecutionAwaitingConfirmation {
		t.Fatalf("after first approval=%#v err=%v", executions, err)
	}
	if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 ||
		!strings.Contains(records[0].Message.Content.TextContent(), "#待办 delete 1（已完成）") || !strings.Contains(records[0].Message.Content.TextContent(), "#待办 delete 2（待确认：2）") {
		t.Fatalf("second confirmation output=%#v err=%v", records, err)
	} else {
		acceptCoordinatorOutputs(t, db, records)
	}

	if err := coordinator.Enqueue(ctx, jobInbound("direct-delete-batch-confirm-2", "确认")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	if handler.calls != 2 || len(handler.allArgs) != 2 || strings.Join(handler.allArgs[1], " ") != "delete 2" {
		t.Fatalf("second destructive execution: calls=%d args=%#v", handler.calls, handler.allArgs)
	}
	executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 || executions[0].State != store.CapabilityExecutionSucceeded || executions[1].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("after second approval=%#v err=%v", executions, err)
	}
	if saved, err := db.GetConversationJob(ctx, job.ID); err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("destructive batch job=%#v err=%v", saved, err)
	}
	if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "#待办 delete 2（已完成）") {
		t.Fatalf("final destructive output=%#v err=%v", records, err)
	}
}

func TestCoordinatorDirectDestructiveBatchRejectsEachOperation(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Text: "不应执行", Kind: "todo"})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-delete-reject-batch", "待办 删除 1,2")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	acceptCoordinatorOutputs(t, db, records)

	for index, eventID := range []string{"direct-delete-reject-1", "direct-delete-reject-2"} {
		if err := coordinator.Enqueue(ctx, jobInbound(eventID, "拒绝")); err != nil {
			t.Fatal(err)
		}
		coordinator.execute(ctx, claimOnlyConversationJob(t, db))
		executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
		if err != nil || len(executions) != 2 {
			t.Fatalf("after rejection %d executions=%#v err=%v", index, executions, err)
		}
		for operationIndex, execution := range executions {
			want := store.CapabilityExecutionAwaitingConfirmation
			if operationIndex <= index {
				want = store.CapabilityExecutionDenied
			}
			if execution.State != want {
				t.Fatalf("after rejection %d execution[%d]=%#v want=%s", index, operationIndex, execution, want)
			}
		}
		if handler.calls != 0 {
			t.Fatalf("rejected destructive batch executed %d times", handler.calls)
		}
		if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "已拒绝") {
			t.Fatalf("rejection %d output=%#v err=%v", index, records, err)
		} else {
			acceptCoordinatorOutputs(t, db, records)
		}
	}
	if saved, err := db.GetConversationJob(ctx, job.ID); err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("rejected destructive batch job=%#v err=%v", saved, err)
	}
}

func TestCoordinatorDirectOrdinaryBatchExecutesAllOperationsInOrder(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Text: "已更新", Kind: "todo"})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-done-batch", "待办 完成 1,2")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	if handler.calls != 2 || len(handler.allArgs) != 2 || strings.Join(handler.allArgs[0], " ") != "done 1" || strings.Join(handler.allArgs[1], " ") != "done 2" {
		t.Fatalf("ordinary batch execution: calls=%d args=%#v", handler.calls, handler.allArgs)
	}
	if len(handler.described) != 2 || len(handler.describedArgs) != 2 || strings.Join(handler.describedArgs[0], " ") != "done 1" || strings.Join(handler.describedArgs[1], " ") != "done 2" {
		t.Fatalf("ordinary batch preflight: invocations=%#v args=%#v", handler.described, handler.describedArgs)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 || executions[0].State != store.CapabilityExecutionSucceeded || executions[1].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("ordinary batch executions=%#v err=%v", executions, err)
	}
	if saved, err := db.GetConversationJob(ctx, job.ID); err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("ordinary batch job=%#v err=%v", saved, err)
	}
	if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 || records[0].Message.Content.TextContent() != "已更新\n\n已更新" {
		t.Fatalf("ordinary batch output=%#v err=%v", records, err)
	}
}

func TestCoordinatorDirectMutationPreflightStartsLoginWithoutPreparingOperation(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{
		describeErr: auth.ErrNotLoggedIn,
		outcome:     commands.AuthRequiredOutcome(commands.Response{Text: "请先登录", Kind: commands.ResponseKindAuthWait}),
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-preflight-auth", "待办 完成 1")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 0 {
		t.Fatalf("preflight auth prepared mutations=%#v err=%v", executions, err)
	}
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateWaitingAuth {
		t.Fatalf("preflight auth job=%#v err=%v", saved, err)
	}
	if handler.calls != 1 || handler.id != commands.CapabilityLogin || len(handler.allArgs) != 1 || len(handler.allArgs[0]) != 0 {
		t.Fatalf("preflight auth login call: calls=%d id=%s args=%#v", handler.calls, handler.id, handler.allArgs)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || records[0].Message.Content.TextContent() != "请先登录" {
		t.Fatalf("preflight auth output=%#v err=%v", records, err)
	}
}

func TestCoordinatorDirectMutationPreflightLoginSuccessLeavesJobRetryable(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{
		describeErr: auth.ErrReauthorizationRequired,
		outcome:     commands.SuccessOutcome(commands.Response{Text: "已登录", Kind: "login"}),
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-preflight-auth-success", "待办 完成 1")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 0 {
		t.Fatalf("preflight auth-success prepared mutations=%#v err=%v", executions, err)
	}
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait {
		t.Fatalf("preflight auth-success job=%#v err=%v", saved, err)
	}
	if handler.calls != 1 || handler.id != commands.CapabilityLogin {
		t.Fatalf("preflight auth-success login call: calls=%d id=%s", handler.calls, handler.id)
	}
}
