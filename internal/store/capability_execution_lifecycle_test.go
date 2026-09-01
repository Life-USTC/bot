package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompletedJobRequiresTerminalCapabilityExecutions(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	job := enqueueConversationJobTest(t, s, ident, "terminal-barrier", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	read, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "terminal-barrier-read",
		Capability: "course", Effect: "read",
	})
	if err != nil || !created {
		t.Fatalf("prepare read=%#v created=%v err=%v", read, created, err)
	}
	if ok, err := s.CompleteConversationJob(ctx, job.ID, claimed.LeaseToken); ok || !errors.Is(err, ErrConversationJobHasNonterminalOperations) {
		t.Fatalf("completed around running operation: ok=%v err=%v", ok, err)
	}
	if got := mustGetConversationJob(t, s, job.ID); got.State != ConversationJobStateRunning || got.LeaseToken != claimed.LeaseToken {
		t.Fatalf("barrier did not roll back job transition: %#v", got)
	}
	if _, err := s.FinishCapabilityExecution(ctx, read.ID, claimed.LeaseToken, "done", nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteConversationJob(ctx, job.ID, claimed.LeaseToken); err != nil || !ok {
		t.Fatalf("complete after terminal operation: ok=%v err=%v", ok, err)
	}
}

func TestCompletedJobMarksOnlyStaleEffectfulExecutionUnknown(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	job := enqueueConversationJobTest(t, s, ident, "terminal-stale-operation", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	first, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	execution, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: first.LeaseToken, DedupeKey: "terminal-stale-mutation",
		Capability: "notify", Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, first.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim mutation=%#v execute=%v err=%v", execution, execute, err)
	}
	if ok, err := s.RetryConversationJob(ctx, job.ID, first.LeaseToken, "retry around stale mutation"); err != nil || !ok {
		t.Fatalf("release first claim ok=%v err=%v", ok, err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, now.Add(time.Second))
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	if ok, err := s.CompleteConversationJob(ctx, job.ID, second.LeaseToken); err != nil || !ok {
		t.Fatalf("complete recovered job ok=%v err=%v", ok, err)
	}
	execution, found, err := s.CapabilityExecution(ctx, execution.ID)
	if err != nil || !found || execution.State != CapabilityExecutionUnknown {
		t.Fatalf("stale mutation outcome=%#v found=%v err=%v", execution, found, err)
	}
}

func TestFailedJobAtomicallyTerminalizesEveryCapability(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	job := enqueueConversationJobTest(t, s, ident, "failed-operation-batch", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	waiting, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, Sequence: 0, DedupeKey: "failed-waiting",
		Capability: "notify", Effect: "write", RequiresConfirmation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, Sequence: 1, DedupeKey: "failed-approved",
		Capability: "notify", Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	running, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, Sequence: 2, DedupeKey: "failed-running",
		Capability: "notify", Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	running, execute, err := s.ClaimCapabilityExecutionForJob(ctx, running.ID, job.ID, claimed.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim running mutation=%#v execute=%v err=%v", running, execute, err)
	}
	read, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, Sequence: 3, DedupeKey: "failed-read",
		Capability: "course", Effect: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.FailConversationJob(ctx, job.ID, claimed.LeaseToken, "contract failure"); err != nil || !ok {
		t.Fatalf("fail job ok=%v err=%v", ok, err)
	}
	for _, want := range []struct {
		id    string
		state CapabilityExecutionState
	}{
		{waiting.ID, CapabilityExecutionFailed},
		{approved.ID, CapabilityExecutionFailed},
		{running.ID, CapabilityExecutionUnknown},
		{read.ID, CapabilityExecutionFailed},
	} {
		got, found, err := s.CapabilityExecution(ctx, want.id)
		if err != nil || !found || got.State != want.state {
			t.Fatalf("terminal operation=%#v found=%v err=%v want=%s", got, found, err, want.state)
		}
	}
}

func TestCapabilityOwnerFinalizersRequireLiveOwningJob(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	job := enqueueConversationJobTest(t, s, ident, "owner-finalizer-fence", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	execution, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "owner-finalizer-operation",
		Capability: "notify", Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, claimed.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim operation=%#v execute=%v err=%v", execution, execute, err)
	}
	if ok, err := s.RetryConversationJob(ctx, job.ID, claimed.LeaseToken, "retry"); err != nil || !ok {
		t.Fatalf("retry job ok=%v err=%v", ok, err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, execution.ID, claimed.LeaseToken, "late", nil); err == nil {
		t.Fatal("late worker finished operation after job released its lease")
	}
	if _, err := s.FinishCapabilityExecutionUnknown(ctx, execution.ID, claimed.LeaseToken, "late", "late"); err == nil {
		t.Fatal("late worker marked operation unknown after job released its lease")
	}
	if _, err := s.DeferCapabilityExecutionForAuth(ctx, execution.ID, claimed.LeaseToken); err == nil {
		t.Fatal("late worker deferred operation after job released its lease")
	}
	if err := s.UpdateCapabilityExecutionReceipt(ctx, execution.ID, claimed.LeaseToken, CapabilityReceipt{Subject: "late"}); err == nil {
		t.Fatal("late worker updated receipt after job released its lease")
	}
}

func TestCapabilityOwnerFinalizersRejectExpiredOwningJobBeforeExpirySweep(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	job := enqueueConversationJobTest(t, s, ident, "expired-owner-finalizer-fence", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	execution, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken, DedupeKey: "expired-owner-operation",
		Capability: "notify", Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, claimed.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim operation=%#v execute=%v err=%v", execution, execute, err)
	}
	if err := s.db.Model(&conversationJobRow{}).Where("id = ?", job.ID).Update("expires_at", now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, execution.ID, claimed.LeaseToken, "late", nil); err == nil {
		t.Fatal("worker finished operation after owning job TTL elapsed")
	}
	if _, err := s.FinishCapabilityExecutionUnknown(ctx, execution.ID, claimed.LeaseToken, "late", "late"); err == nil {
		t.Fatal("worker marked operation unknown after owning job TTL elapsed")
	}
	if _, err := s.DeferCapabilityExecutionForAuth(ctx, execution.ID, claimed.LeaseToken); err == nil {
		t.Fatal("worker deferred operation after owning job TTL elapsed")
	}
	if err := s.UpdateCapabilityExecutionReceipt(ctx, execution.ID, claimed.LeaseToken, CapabilityReceipt{Subject: "late"}); err == nil {
		t.Fatal("worker updated receipt after owning job TTL elapsed")
	}
}

func TestRunningReadPreparationRequiresLease(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := t.Context()
	ident := conversationJobTestIdentity()
	job := enqueueConversationJobTest(t, s, ident, "unbound-read", ConversationJobEnqueue{ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if _, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, DedupeKey: "unbound-read-operation", Capability: "course", Effect: "read",
	}); err == nil || created {
		t.Fatalf("unbound running read created=%v err=%v", created, err)
	}
}

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
	adopted, execute, err := s.ClaimCapabilityExecutionForJob(ctx, read.ID, job.ID, second.LeaseToken)
	if err != nil || !execute || adopted.LeaseToken != second.LeaseToken {
		t.Fatalf("running read was not adopted by current lease: execution=%#v execute=%v err=%v", adopted, execute, err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, adopted.ID, first.LeaseToken, "stale", nil); err == nil {
		t.Fatal("stale read worker finalized the adopted execution")
	}
	if finished, err := s.FinishCapabilityExecution(ctx, adopted.ID, adopted.LeaseToken, "current", nil); err != nil || finished.Result != "current" {
		t.Fatalf("current read finish=%#v err=%v", finished, err)
	}
}

func TestStaleWorkerCannotMarkCurrentMutationUnknownOrFinishIt(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := enqueueConversationJobTest(t, s, ident, "stale-mutation-owner", ConversationJobEnqueue{ExpiresAt: now.Add(time.Hour)})
	first, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	execution, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: first.LeaseToken, DedupeKey: "stale-mutation-owner-operation",
		Capability: "notify", Effect: "write",
	})
	if err != nil || !created {
		t.Fatalf("prepare execution=%#v created=%v err=%v", execution, created, err)
	}
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, first.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("first operation claim=%#v execute=%v err=%v", execution, execute, err)
	}
	if _, err := s.DeferCapabilityExecutionForAuth(ctx, execution.ID, execution.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TransitionConversationJob(ctx, job.ID, first.LeaseToken, ConversationJobTransition{
		State: ConversationJobStateWaitingAuth, WaitReason: ConversationJobWaitReasonAuth,
	}); err != nil || !ok {
		t.Fatalf("wait for auth ok=%v err=%v", ok, err)
	}
	if err := s.UnblockConversationJobsAfterAuth(ctx, ident, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, now.Add(time.Minute))
	if err != nil || second == nil || second.LeaseToken == first.LeaseToken {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	execution, execute, err = s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, second.LeaseToken)
	if err != nil || !execute || execution.LeaseToken != second.LeaseToken {
		t.Fatalf("second operation claim=%#v execute=%v err=%v", execution, execute, err)
	}
	current, marked, err := s.MarkStaleCapabilityExecutionUnknown(ctx, execution.ID, job.ID, first.LeaseToken, "stale worker")
	if err != nil || marked || current.State != CapabilityExecutionRunning || current.LeaseToken != second.LeaseToken {
		t.Fatalf("stale unknown mark current=%#v marked=%v err=%v", current, marked, err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, execution.ID, first.LeaseToken, "stale", nil); err == nil {
		t.Fatal("stale mutation worker finalized current execution")
	}
	if finished, err := s.FinishCapabilityExecution(ctx, execution.ID, second.LeaseToken, "current", nil); err != nil || finished.Result != "current" {
		t.Fatalf("current mutation finish=%#v err=%v", finished, err)
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
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, claimed.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim execution=%v err=%v", execute, err)
	}
	finished, err := s.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, "服务已受理，但结果无法确认", "transport timeout")
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
