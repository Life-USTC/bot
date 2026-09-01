package store

import (
	"context"
	"testing"
	"time"
)

func TestCapabilityClaimRequiresCurrentLeaseAndCancellationTerminalizesBatch(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "cancel-operations", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim job=%#v err=%v", claimed, err)
	}

	running, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "cancel-running",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created {
		t.Fatalf("prepare running operation=%#v created=%v err=%v", running, created, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, running.ID, job.ID, "stale-token"); err != nil || execute {
		t.Fatalf("stale capability claim execute=%v err=%v", execute, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, running.ID, job.ID, claimed.LeaseToken); err != nil || !execute {
		t.Fatalf("current capability claim execute=%v err=%v", execute, err)
	}
	claimedAgain, execute, err := s.ClaimCapabilityExecutionForJob(ctx, running.ID, job.ID, claimed.LeaseToken)
	if err != nil || execute || claimedAgain.State != CapabilityExecutionRunning || claimedAgain.LeaseToken != claimed.LeaseToken {
		t.Fatalf("same-lease loser changed active operation=%#v execute=%v err=%v", claimedAgain, execute, err)
	}

	approved, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "cancel-approved",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created || approved.State != CapabilityExecutionApproved {
		t.Fatalf("prepare approved operation=%#v created=%v err=%v", approved, created, err)
	}
	waiting, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "cancel-waiting",
		Capability: "notify", Effect: "write", RequiresConfirmation: true,
	})
	if err != nil || !created || waiting.State != CapabilityExecutionAwaitingConfirmation {
		t.Fatalf("prepare waiting operation=%#v created=%v err=%v", waiting, created, err)
	}
	read, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "cancel-read",
		Capability: "course", Effect: "read",
	})
	if err != nil || !created || read.State != CapabilityExecutionRunning {
		t.Fatalf("prepare read operation=%#v created=%v err=%v", read, created, err)
	}

	cancelled, err := s.CancelConversationJob(ctx, job.ID, "用户取消")
	if err != nil || !cancelled {
		t.Fatalf("cancel job=%v err=%v", cancelled, err)
	}
	gotJob := mustGetConversationJob(t, s, job.ID)
	if gotJob.State != ConversationJobStateCancelled || gotJob.LeaseToken != "" {
		t.Fatalf("cancelled job=%#v", gotJob)
	}
	for _, want := range []struct {
		id    string
		state CapabilityExecutionState
	}{
		{running.ID, CapabilityExecutionUnknown},
		{approved.ID, CapabilityExecutionCancelled},
		{waiting.ID, CapabilityExecutionCancelled},
		{read.ID, CapabilityExecutionCancelled},
	} {
		got, found, err := s.CapabilityExecution(ctx, want.id)
		if err != nil || !found || got.State != want.state || got.FinishedAt == nil {
			t.Fatalf("cancelled operation=%#v found=%v err=%v want=%s", got, found, err, want.state)
		}
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, approved.ID, job.ID, claimed.LeaseToken); err != nil || execute {
		t.Fatalf("cancelled approved operation was claimable: execute=%v err=%v", execute, err)
	}
}

func TestReadCapabilityExecutionRemainsReplayableAfterLeaseRecovery(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "recover-read", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	first, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	read, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: first.LeaseToken, DedupeKey: "recover-read-operation",
		Capability: "course", Effect: "read",
	})
	if err != nil || !created || read.State != CapabilityExecutionRunning || read.LeaseToken != first.LeaseToken {
		t.Fatalf("prepare read execution=%#v created=%v err=%v", read, created, err)
	}
	when := now.Add(3 * time.Minute)
	if err := s.RecoverConversationJobLeases(ctx, when, time.Minute); err != nil {
		t.Fatal(err)
	}
	read, found, err := s.CapabilityExecution(ctx, read.ID)
	if err != nil || !found || read.State != CapabilityExecutionRunning || read.LeaseToken != first.LeaseToken {
		t.Fatalf("recovered read execution=%#v found=%v err=%v", read, found, err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, when)
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, read.ID, job.ID, second.LeaseToken); err != nil || execute {
		t.Fatalf("running read unexpectedly claimed again: execute=%v err=%v", execute, err)
	}
}

func TestUnknownCapabilityOutcomePreservesDescriptorResult(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "unknown-result", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim job=%#v err=%v", claimed, err)
	}
	execution, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "unknown-result-operation",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created {
		t.Fatalf("prepare execution=%#v created=%v err=%v", execution, created, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, claimed.LeaseToken); err != nil || !execute {
		t.Fatalf("claim execution=%v err=%v", execute, err)
	}
	finished, err := s.FinishCapabilityExecutionUnknown(ctx, execution.ID, "服务已受理，但结果无法确认", "transport timeout")
	if err != nil || finished.State != CapabilityExecutionUnknown || finished.Result != "服务已受理，但结果无法确认" || finished.Error != "transport timeout" {
		t.Fatalf("unknown execution=%#v err=%v", finished, err)
	}
}

func TestCapabilityClaimRejectsRecoveredLeaseAndAcceptsCurrentLease(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "lease-race", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	first, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	execution, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: first.LeaseToken, DedupeKey: "lease-race-operation",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created || execution.State != CapabilityExecutionApproved {
		t.Fatalf("prepare execution=%#v created=%v err=%v", execution, created, err)
	}
	recoveredAt := now.Add(3 * time.Minute)
	if err := s.RecoverConversationJobLeases(ctx, recoveredAt, time.Minute); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, recoveredAt)
	if err != nil || second == nil || second.LeaseToken == first.LeaseToken {
		t.Fatalf("second claim=%#v first=%#v err=%v", second, first, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, first.LeaseToken); err != nil || execute {
		t.Fatalf("stale lease claimed operation: execute=%v err=%v", execute, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, second.LeaseToken); err != nil || !execute {
		t.Fatalf("current lease failed to claim operation: execute=%v err=%v", execute, err)
	}
}

func TestCapabilityPreparationBatchRollsBackBeforeWritingAnyRow(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	job := enqueueConversationJobTest(t, s, ident, "batch-rollback", ConversationJobEnqueue{ExpiresAt: time.Now().UTC().Add(time.Hour)})
	_, _, err := s.PrepareCapabilityExecutions(ctx, []CapabilityExecutionPrepare{
		{Identity: ident, JobID: job.ID, Sequence: 0, DedupeKey: "batch-first", Capability: "notify", Effect: "write"},
		{Identity: ident, JobID: job.ID, Sequence: 1, Capability: "notify", Effect: "write"},
	})
	if err == nil {
		t.Fatal("invalid batch unexpectedly committed")
	}
	operations, err := s.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 {
		t.Fatalf("partially committed capability batch=%#v", operations)
	}
}

func TestCapabilityExpiryTerminalizesApprovedAndRunningOperations(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "expire-operations", ConversationJobEnqueue{ExpiresAt: now.Add(time.Second)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim job=%#v err=%v", claimed, err)
	}
	running, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "expire-running",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created {
		t.Fatalf("prepare operation=%#v created=%v err=%v", running, created, err)
	}
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, running.ID, job.ID, claimed.LeaseToken); err != nil || !execute {
		t.Fatalf("claim operation execute=%v err=%v", execute, err)
	}
	approved, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "expire-approved",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created {
		t.Fatalf("prepare approved operation=%#v created=%v err=%v", approved, created, err)
	}
	read, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "expire-read",
		Capability: "course", Effect: "read",
	})
	if err != nil || !created || read.State != CapabilityExecutionRunning {
		t.Fatalf("prepare read operation=%#v created=%v err=%v", read, created, err)
	}

	if err := s.ExpireConversationJobs(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, job.ID); got.State != ConversationJobStateExpired {
		t.Fatalf("expired job=%#v", got)
	}
	got, found, err := s.CapabilityExecution(ctx, running.ID)
	if err != nil || !found || got.State != CapabilityExecutionUnknown {
		t.Fatalf("expired running operation=%#v found=%v err=%v", got, found, err)
	}
	got, found, err = s.CapabilityExecution(ctx, approved.ID)
	if err != nil || !found || got.State != CapabilityExecutionExpired {
		t.Fatalf("expired approved operation=%#v found=%v err=%v", got, found, err)
	}
	got, found, err = s.CapabilityExecution(ctx, read.ID)
	if err != nil || !found || got.State != CapabilityExecutionExpired {
		t.Fatalf("expired read operation=%#v found=%v err=%v", got, found, err)
	}
}
