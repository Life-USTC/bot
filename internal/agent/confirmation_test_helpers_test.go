package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

func commitAgentConfirmationReceipt(t *testing.T, db *store.Store, ctx context.Context, ident store.Identity, jobID int64, leaseToken, dedupeKey string) {
	t.Helper()
	executions, err := db.CapabilityExecutionsForJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var executionID string
	for _, execution := range executions {
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			executionID = execution.ID
			break
		}
	}
	if executionID == "" {
		t.Fatalf("no awaiting confirmation operation for job %d", jobID)
	}
	outputs, err := db.CommitConversationJobOutput(ctx, store.ConversationJobOutputCommit{
		JobID: jobID, LeaseToken: leaseToken,
		Messages: []message.Outbound{{
			Kind:    "confirmation",
			Target:  message.Conversation{Platform: ident.Platform, Type: ident.ConversationType, ID: ident.ConversationID},
			Content: message.Content{Text: "请确认 #待确认操作{" + executionID + "}"}, DedupeKey: dedupeKey,
		}},
		ReceiptIDs: []string{executionID},
		Transition: store.ConversationJobTransition{State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 {
		t.Fatalf("confirmation output count=%d", len(outputs))
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID != outputs[0].Record.ID {
			continue
		}
		if err := db.Complete(ctx, record.ID, delivery.Outcome{
			State:   delivery.OutcomeAccepted,
			Receipt: message.Receipt{AcceptedAt: time.Now().UTC()},
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("confirmation outbox %d was not claimable", outputs[0].Record.ID)
}
