package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestTargetPreflightRequestsLoginBeforePreparingMutation(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "preflight-auth", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimConversationJob(t.Context(), ident)
	if err != nil || claimed == nil {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	ctx := store.WithConversationJobLease(t.Context(), job.ID, claimed.LeaseToken)
	svc := &Service{handler: commands.Handler{Store: db}}
	result, err := svc.invokeHostCapability(ctx, hostCapabilityInput{Capability: "todo", Arguments: []string{"delete", "1"}}, ident, job.ID, nil)
	if err != nil || !json.Valid([]byte(result)) || !strings.Contains(result, `"status": "auth_required"`) {
		t.Fatalf("result=%s err=%v", result, err)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 0 {
		t.Fatalf("unresolved targets were prepared: %#v err=%v", executions, err)
	}
}
