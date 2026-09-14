package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestPresentationFailurePreservesBusinessSuccess(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "approved"}[approved], func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "delivery", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{handler: commands.Handler{Store: db, EnableImageResponses: true}}
			invocation := commands.ParseCommand("help").Invocation
			deliveryErr := errors.New("outbox unavailable")
			send := func(context.Context, store.Identity, commands.Response) error { return deliveryErr }
			var executionID string
			if approved {
				claimed, err := db.ClaimConversationJob(t.Context(), ident)
				if err != nil || claimed == nil {
					t.Fatalf("claim=%#v err=%v", claimed, err)
				}
				execution, _, err := db.PrepareCapabilityExecution(t.Context(), store.CapabilityExecutionPrepare{Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "approved", Capability: "help", Effect: "read"})
				if err != nil {
					t.Fatal(err)
				}
				executionID = execution.ID
				_, _, err = svc.executeApprovedCapability(t.Context(), execution, ident, send)
				if !errors.Is(err, deliveryErr) {
					t.Fatalf("error=%v", err)
				}
			} else {
				claimed, claimErr := db.ClaimConversationJob(t.Context(), ident)
				if claimErr != nil || claimed == nil {
					t.Fatalf("claim=%#v err=%v", claimed, claimErr)
				}
				ctx := store.WithConversationJobLease(t.Context(), job.ID, claimed.LeaseToken)
				_, id, _, err := svc.executeUnconfirmedHostCapability(ctx, invocation, ident, job.ID, "read", send)
				executionID = id
				if !errors.Is(err, deliveryErr) {
					t.Fatalf("error=%v", err)
				}
			}
			execution, _, err := db.CapabilityExecution(t.Context(), executionID)
			if err != nil {
				t.Fatal(err)
			}
			if execution.State != store.CapabilityExecutionSucceeded || !strings.Contains(execution.Result, `"status":"succeeded"`) {
				t.Fatalf("business result changed by presentation failure: %#v", execution)
			}
		})
	}
}
