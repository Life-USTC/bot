package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestConversationJobEnqueueIsIdempotentAndSequencesPerConversation(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	expires := time.Now().UTC().Add(time.Hour)

	first, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity:      ident,
		SourceEventID: "event-1",
		Input:         ConversationJobInput{Text: "查课表"},
		Invocation:    ConversationJobInvocation{Name: "schedule", Args: []string{"today"}},
		ExpiresAt:     expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.ID <= 0 || first.Sequence != 1 || first.Revision != 1 || first.State != ConversationJobStateQueued {
		t.Fatalf("first enqueue = %#v, created=%v", first, created)
	}
	if first.Input.Text != "查课表" || first.Invocation.Name != "schedule" || first.InputJSON == "" || first.InvocationJSON == "" {
		t.Fatalf("typed JSON fields were not persisted: %#v", first)
	}

	duplicate, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity:      ident,
		SourceEventID: " event-1 ",
		InputJSON:     `{"text":"different"}`,
		ExpiresAt:     expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || duplicate.ID != first.ID || duplicate.Sequence != first.Sequence || duplicate.Input.Text != first.Input.Text {
		t.Fatalf("duplicate enqueue = %#v, created=%v", duplicate, created)
	}

	second, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity:      ident,
		SourceEventID: "event-2",
		InputJSON:     `{"text":"明天"}`,
		InvocationJSON: `{
			"command": "schedule"
		}`,
		ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || second.Sequence != 2 {
		t.Fatalf("second enqueue = %#v, created=%v", second, created)
	}

	other := ident
	other.ConversationID = "other"
	third, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity:      other,
		SourceEventID: "event-3",
		ExpiresAt:     expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || third.Sequence != 1 {
		t.Fatalf("other conversation sequence = %#v, created=%v", third, created)
	}
}

func TestConversationJobFIFOAndLeaseCAS(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)

	first := enqueueConversationJobTest(t, s, ident, "fifo-1", ConversationJobEnqueue{ExpiresAt: expires})
	second := enqueueConversationJobTest(t, s, ident, "fifo-2", ConversationJobEnqueue{ExpiresAt: expires})

	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != first.ID || claimed.Sequence != 1 || claimed.Attempts != 1 || claimed.LeaseToken == "" {
		t.Fatalf("first claim = %#v", claimed)
	}
	if next, err := s.ClaimConversationJob(ctx, ident, now); err != nil {
		t.Fatal(err)
	} else if next != nil {
		t.Fatalf("later FIFO job bypassed running predecessor: %#v", next)
	}

	if ok, err := s.TransitionConversationJob(ctx, first.ID, "stale-token", ConversationJobTransition{
		State: ConversationJobStateWaitingAuth,
	}); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("stale lease token changed the job")
	}
	if ok, err := s.TransitionConversationJob(ctx, first.ID, claimed.LeaseToken, ConversationJobTransition{
		State: ConversationJobStateWaitingAuth,
	}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("valid lease token did not transition the job")
	}
	if got := mustGetConversationJob(t, s, first.ID); got.State != ConversationJobStateWaitingAuth || got.WaitReason != ConversationJobWaitReasonAuth || got.LeaseToken != "" {
		t.Fatalf("waiting auth job = %#v", got)
	}
	if next, err := s.ClaimConversationJob(ctx, ident, now); err != nil {
		t.Fatal(err)
	} else if next != nil {
		t.Fatalf("job after waiting predecessor was claimed: %#v", next)
	}
	if err := s.UnblockConversationJobsAfterAuth(ctx, ident, now); err != nil {
		t.Fatal(err)
	}
	claimed, err = s.ClaimConversationJob(ctx, ident, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != first.ID || claimed.Revision != 2 || claimed.Attempts != 2 {
		t.Fatalf("unblocked first claim = %#v", claimed)
	}
	if ok, err := s.CompleteConversationJob(ctx, first.ID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("valid completion CAS failed")
	}
	if ok, err := s.CompleteConversationJob(ctx, first.ID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("terminal job was completed twice")
	}

	claimed, err = s.ClaimConversationJob(ctx, ident, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != second.ID || claimed.Sequence != 2 {
		t.Fatalf("second FIFO claim = %#v", claimed)
	}
}

func TestConversationJobConcurrentClaimsAreExclusive(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	job := enqueueConversationJobTest(t, s, ident, "claim-once", ConversationJobEnqueue{ExpiresAt: time.Now().UTC().Add(time.Hour)})
	now := time.Now().UTC()

	const workers = 12
	var wg sync.WaitGroup
	claims := make(chan *ConversationJob, workers)
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ClaimConversationJob(ctx, ident, now)
			if err != nil {
				errs <- err
				return
			}
			if claimed != nil {
				claims <- claimed
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var claimed []*ConversationJob
	for job := range claims {
		claimed = append(claimed, job)
	}
	if len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("concurrent claims = %#v", claimed)
	}
}

func TestCapabilityConfirmationAndAuthReleaseAreOnceOnly(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	confirmation := enqueueConversationJobTest(t, s, ident, "confirm", ConversationJobEnqueue{
		State:     ConversationJobStateWaitingConfirmation,
		ExpiresAt: expires,
	})
	operation, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: confirmation.ID, DedupeKey: "confirm-once", Capability: "logout",
		Effect: "destructive", RequiresConfirmation: true,
	})
	if err != nil || !created {
		t.Fatalf("prepare confirmation: created=%v err=%v", created, err)
	}

	const consumers = 10
	var wg sync.WaitGroup
	consumed := make(chan *ConversationJob, consumers)
	resolved := make(chan *CapabilityExecution, consumers)
	errs := make(chan error, consumers)
	for range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execution, job, err := s.ResolveCapabilityConfirmation(ctx, ident, CapabilityConfirmationDecision{Approved: true}, now)
			if err != nil {
				errs <- err
				return
			}
			if job != nil {
				consumed <- job
			}
			if execution != nil {
				resolved <- execution
			}
		}()
	}
	wg.Wait()
	close(consumed)
	close(resolved)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var confirmations []*ConversationJob
	for job := range consumed {
		confirmations = append(confirmations, job)
	}
	if len(confirmations) != 1 || confirmations[0].ID != confirmation.ID || confirmations[0].Revision != 2 || confirmations[0].State != ConversationJobStateQueued {
		t.Fatalf("confirmation consumes = %#v", confirmations)
	}
	var operations []*CapabilityExecution
	for execution := range resolved {
		operations = append(operations, execution)
	}
	if len(operations) != 1 || operations[0].ID != operation.ID || operations[0].State != CapabilityExecutionApproved {
		t.Fatalf("confirmation resolutions = %#v", operations)
	}
	if got := mustGetConversationJob(t, s, confirmation.ID); got.WaitReason != ConversationJobWaitReasonNone {
		t.Fatalf("consumed confirmation wait reason = %q", got.WaitReason)
	}

	auth := enqueueConversationJobTest(t, s, ident, "auth", ConversationJobEnqueue{
		State:     ConversationJobStateWaitingAuth,
		ExpiresAt: expires,
	})
	if err := s.UnblockConversationJobsAfterAuth(ctx, ident, now); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, auth.ID); got.State != ConversationJobStateQueued || got.Revision != 2 || got.WaitReason != ConversationJobWaitReasonNone {
		t.Fatalf("auth release = %#v", got)
	}
	if err := s.UnblockConversationJobsAfterAuth(ctx, ident, now); err != nil {
		t.Fatal(err)
	}
}

func TestGroupConversationWaitsAreScopedToActor(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	first := Identity{Platform: "napcat", UserID: "41", ConversationType: "group", ConversationID: "100"}
	second := first
	second.UserID = "42"

	waiting := enqueueConversationJobTest(t, s, first, "actor-one-confirm", ConversationJobEnqueue{
		State: ConversationJobStateWaitingConfirmation, ExpiresAt: expires,
	})
	if _, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: first, JobID: waiting.ID, DedupeKey: "actor-one-confirm", Capability: "logout",
		Effect: "destructive", RequiresConfirmation: true,
	}); err != nil || !created {
		t.Fatalf("prepare actor confirmation: created=%v err=%v", created, err)
	}
	queued := enqueueConversationJobTest(t, s, second, "actor-two-command", ConversationJobEnqueue{ExpiresAt: expires})

	claimed, err := s.ClaimNextConversationJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("second actor was blocked by first actor: %#v", claimed)
	}
	if ok, err := s.CompleteConversationJob(ctx, claimed.ID, claimed.LeaseToken); err != nil || !ok {
		t.Fatalf("complete second actor job: ok=%v err=%v", ok, err)
	}

	_, consumed, err := s.ResolveCapabilityConfirmation(ctx, second, CapabilityConfirmationDecision{Approved: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatalf("second actor consumed first actor confirmation: %#v", consumed)
	}
	_, consumed, err = s.ResolveCapabilityConfirmation(ctx, first, CapabilityConfirmationDecision{Approved: true}, now)
	if err != nil || consumed == nil || consumed.ID != waiting.ID {
		t.Fatalf("first actor confirmation = %#v err=%v", consumed, err)
	}

	auth := enqueueConversationJobTest(t, s, first, "actor-one-auth", ConversationJobEnqueue{
		State: ConversationJobStateWaitingAuth, ExpiresAt: expires,
	})
	if err := s.UnblockConversationJobsAfterAuth(ctx, second, now); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, auth.ID); got.State != ConversationJobStateWaitingAuth {
		t.Fatalf("second actor released first actor auth wait: %#v", got)
	}
}

func TestConversationJobInputResumeLeaseRecoveryExpiryAndTerminalProtection(t *testing.T) {
	s := openConversationJobTestStore(t)
	ctx := context.Background()
	ident := conversationJobTestIdentity()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)

	waitingInput := enqueueConversationJobTest(t, s, ident, "input", ConversationJobEnqueue{
		State:     ConversationJobStateWaitingInput,
		ExpiresAt: expires,
	})
	if ok, err := s.ResumeConversationJobInput(ctx, waitingInput.ID, ConversationJobInput{Text: "补充参数"}, now); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("waiting input was not resumed")
	}
	if ok, err := s.ResumeConversationJobInput(ctx, waitingInput.ID, ConversationJobInput{Text: "duplicate"}, now); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("waiting input was resumed twice")
	}
	if got := mustGetConversationJob(t, s, waitingInput.ID); got.State != ConversationJobStateQueued || got.Input.Text != "补充参数" {
		t.Fatalf("resumed input = %#v", got)
	}

	running := enqueueConversationJobTest(t, s, ident, "recover", ConversationJobEnqueue{ExpiresAt: expires})
	claimed, err := s.ClaimConversationJob(ctx, ident, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != waitingInput.ID {
		t.Fatalf("first queued job claim = %#v", claimed)
	}
	if ok, err := s.CompleteConversationJob(ctx, claimed.ID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("input job completion failed")
	}
	claimed, err = s.ClaimConversationJob(ctx, ident, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != running.ID {
		t.Fatalf("recoverable job claim = %#v", claimed)
	}
	recoveryAt := now.Add(3 * time.Minute)
	if err := s.RecoverConversationJobLeases(ctx, recoveryAt, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, running.ID); got.State != ConversationJobStateRetryWait || got.Revision != 1 || got.LeaseToken != "" || got.LastError == "" {
		t.Fatalf("recovered job = %#v", got)
	}
	claimed, err = s.ClaimConversationJob(ctx, ident, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != running.ID || claimed.Revision != 1 || claimed.Attempts != 2 {
		t.Fatalf("reclaimed job = %#v", claimed)
	}
	if ok, err := s.CompleteConversationJob(ctx, claimed.ID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("reclaimed job completion failed")
	}
	if err := s.ExpireConversationJobs(ctx, recoveryAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, running.ID); got.State != ConversationJobStateCompleted {
		t.Fatalf("terminal job changed during expiry = %#v", got)
	}

	expiring := enqueueConversationJobTest(t, s, ident, "expire", ConversationJobEnqueue{ExpiresAt: now.Add(time.Second)})
	if err := s.ExpireConversationJobs(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := mustGetConversationJob(t, s, expiring.ID); got.State != ConversationJobStateExpired || got.LeaseToken != "" {
		t.Fatalf("expired job = %#v", got)
	}
	if claimed, err := s.ClaimConversationJob(ctx, ident, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	} else if claimed != nil {
		t.Fatalf("expired job was claimable = %#v", claimed)
	}
}

func openConversationJobTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func conversationJobTestIdentity() Identity {
	return Identity{Platform: "napcat", UserID: "user-1", ConversationType: "private", ConversationID: "conversation-1"}
}

func enqueueConversationJobTest(t *testing.T, s *Store, ident Identity, source string, input ConversationJobEnqueue) ConversationJob {
	t.Helper()
	input.Identity = ident
	input.SourceEventID = source
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().UTC().Add(time.Hour)
	}
	job, created, err := s.EnqueueConversationJob(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("test enqueue %q was deduplicated", source)
	}
	return job
}

func mustGetConversationJob(t *testing.T, s *Store, id int64) *ConversationJob {
	t.Helper()
	job, err := s.GetConversationJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatalf("conversation job %d is missing", id)
	}
	return job
}
