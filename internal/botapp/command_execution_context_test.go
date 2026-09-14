package botapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.Text, confirmationPrompt) {
		t.Fatalf("confirmation output=%#v err=%v", records, err)
	}

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
	if records, err = db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.Text, "#已退出账户") {
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
	if _, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil {
		t.Fatal(err)
	}
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
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.Text, "用户拒绝执行") {
		t.Fatalf("rejection output=%#v err=%v", records, err)
	}
}
