package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestCommandImagesRemainSavedUntilModelSelectsThem(t *testing.T) {
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
			send := func(context.Context, store.Identity, commands.Response) error {
				t.Error("image command sent presentation before model selection")
				return deliveryErr
			}
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
				if err != nil {
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
				if err != nil {
					t.Fatalf("error=%v", err)
				}
			}
			execution, _, err := db.CapabilityExecution(t.Context(), executionID)
			if err != nil {
				t.Fatal(err)
			}
			var result struct{ Images []struct{ ID string } }
			if err := json.Unmarshal([]byte(execution.Result), &result); err != nil || len(result.Images) != 1 {
				t.Fatalf("missing image reference: %s, %v", execution.Result, err)
			}
			image, found, err := db.CommandImage(t.Context(), ident, result.Images[0].ID)
			if err != nil || !found || image.Kind != "help" {
				t.Fatalf("saved image=%#v found=%v err=%v", image, found, err)
			}
			if execution.State != store.CapabilityExecutionSucceeded || !strings.Contains(execution.Result, `"status": "succeeded"`) {
				t.Fatalf("business result changed by presentation failure: %#v", execution)
			}
		})
	}
}
