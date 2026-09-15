package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func TestToolReceiptsPersistDiscoveryAndFailuresWithoutDuplicatingDomainExecution(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "receipts", ConversationType: "private", ConversationID: "receipts"}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "receipts", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimConversationJob(t.Context(), ident)
	if err != nil || claim == nil {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
	ctx := store.WithConversationJobLease(t.Context(), job.ID, claim.LeaseToken)
	svc := &Service{handler: commands.Handler{Store: db}}
	names := []string{"search_campus_tools", "list_campus_resources", "read_campus_resource", "list_campus_prompts", "get_campus_prompt", "search_bot_commands", "get_current_time"}
	for i, name := range names {
		input := &compose.ToolInput{Name: name, CallID: fmt.Sprint(i), Arguments: `{"query":"test"}`}
		endpoint := svc.toolReceiptMiddleware(ident, job.ID)(toolResultMiddleware(nil)(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
			return &compose.ToolOutput{Result: `{"data":"test"}`}, nil
		}))
		if _, err := endpoint(ctx, input); err != nil {
			t.Fatal(err)
		}
		if _, err := endpoint(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	failed := &compose.ToolInput{Name: campusCallToolName, CallID: "setup-failed", Arguments: `{"name":"section_search","arguments":{"query":"数学分析"}}`}
	result := toolresult.Encode("mcp", "section_search", "failed", time.Now(), nil, errors.New("upstream unavailable"))
	if err := svc.saveToolReceipt(ctx, ident, job.ID, failed, result); err != nil {
		t.Fatal(err)
	}
	domain, _, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{Identity: ident, JobID: job.ID, LeaseToken: claim.LeaseToken, DedupeKey: "domain", ToolCallID: "domain-call", Capability: "mcp:update_todo", Arguments: []string{`{"id":"one"}`}, Effect: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.saveToolReceipt(ctx, ident, job.ID, &compose.ToolInput{Name: campusCallToolName, CallID: domain.ToolCallID, Arguments: `{"name":"update_todo","arguments":{"id":"one"}}`}, result); err != nil {
		t.Fatal(err)
	}
	if err := svc.saveToolReceipt(ctx, ident, job.ID, &compose.ToolInput{Name: campusCallToolName, CallID: "new-model-call", Arguments: `{"name":"update_todo","arguments":{"id":"one"}}`}, result); err != nil {
		t.Fatal(err)
	}
	rows, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(rows) != len(names)+2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for i, name := range names {
		if rows[i].Capability != "tool:"+name || rows[i].State != store.CapabilityExecutionSucceeded {
			t.Fatalf("row=%+v", rows[i])
		}
	}
	if rows[len(names)].Capability != "mcp:section_search" || rows[len(names)].State != store.CapabilityExecutionFailed {
		t.Fatalf("failed=%+v", rows[len(names)])
	}
	// Preparing a receipt and losing the worker before finishing must remain
	// recoverable through the ordinary read lease, rather than blocking the job.
	orphan, _, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{Identity: ident, JobID: job.ID, LeaseToken: claim.LeaseToken, DedupeKey: fmt.Sprintf("conversation-job:%d:tool-receipt:orphan", job.ID), ToolCallID: "orphan", Capability: "tool:get_current_time", Arguments: []string{`{}`}, Effect: "read"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RetryConversationJob(ctx, job.ID, claim.LeaseToken, "restart"); err != nil || !ok {
		t.Fatalf("retry=%v err=%v", ok, err)
	}
	claim, err = db.ClaimConversationJob(t.Context(), ident)
	if err != nil || claim == nil {
		t.Fatalf("reclaim=%v err=%v", claim, err)
	}
	ctx = store.WithConversationJobLease(t.Context(), job.ID, claim.LeaseToken)
	result = toolresult.Encode("host", "get_current_time", "succeeded", time.Now(), "now", nil)
	if err := svc.saveToolReceipt(ctx, ident, job.ID, &compose.ToolInput{Name: "get_current_time", CallID: "orphan", Arguments: `{}`}, result); err != nil {
		t.Fatal(err)
	}
	orphan, _, err = db.CapabilityExecution(ctx, orphan.ID)
	if err != nil || orphan.State != store.CapabilityExecutionSucceeded {
		t.Fatalf("orphan=%+v err=%v", orphan, err)
	}
}
