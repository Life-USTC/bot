package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAgentModelAttemptReservationSurvivesInterruptedRun(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "attempts", ConversationType: "private", ConversationID: "attempts"}
	job, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "attempt-crash", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: job=%#v created=%v err=%v", job, created, err)
	}
	if _, err := s.ClaimConversationJob(ctx, ident); err != nil {
		t.Fatal(err)
	}
	first, err := s.RecordAgentRun(ctx, ident, AgentRun{JobID: job.ID, RawText: "crash"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		reserved, err := s.ReserveAgentModelAttempt(ctx, first, job.ID, 5)
		if err != nil || !reserved {
			t.Fatalf("reserve attempt %d: reserved=%v err=%v", i, reserved, err)
		}
	}
	if interrupted, err := s.InterruptStartedAgentRuns(ctx); err != nil || interrupted != 1 {
		t.Fatalf("interrupt started run: interrupted=%d err=%v", interrupted, err)
	}

	second, err := s.RecordAgentRun(ctx, ident, AgentRun{JobID: job.ID, RawText: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		reserved, err := s.ReserveAgentModelAttempt(ctx, second, job.ID, 5)
		if err != nil || !reserved {
			t.Fatalf("resume reserve attempt %d: reserved=%v err=%v", i, reserved, err)
		}
	}
	if reserved, err := s.ReserveAgentModelAttempt(ctx, second, job.ID, 5); err != nil || reserved {
		t.Fatalf("resume exceeded durable limit: reserved=%v err=%v", reserved, err)
	}
	if err := s.FinishAgentRun(ctx, second, AgentRunStatusFailed, "", nil, AgentSpending{ModelRequests: 1}); err != nil {
		t.Fatal(err)
	}
	spending, err := s.AgentJobSpending(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spending.ModelRequests != 5 {
		t.Fatalf("job spending after crash/resume = %#v, want 5 attempts", spending)
	}
}

func TestAgentModelAttemptReservationIsExclusiveAcrossWorkers(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "overlap", ConversationType: "private", ConversationID: "overlap"}
	job, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "attempt-overlap", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: job=%#v created=%v err=%v", job, created, err)
	}
	first, err := s.RecordAgentRun(ctx, ident, AgentRun{JobID: job.ID, RawText: "worker one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordAgentRun(ctx, ident, AgentRun{JobID: job.ID, RawText: "worker two"})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 12
	var wg sync.WaitGroup
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		runID := first
		if i%2 == 1 {
			runID = second
		}
		go func() {
			defer wg.Done()
			reserved, err := s.ReserveAgentModelAttempt(ctx, runID, job.ID, 5)
			if err != nil {
				errs <- err
				return
			}
			results <- reserved
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	reserved := 0
	for result := range results {
		if result {
			reserved++
		}
	}
	if reserved != 5 {
		t.Fatalf("concurrent reservations = %d, want 5", reserved)
	}
	spending, err := s.AgentJobSpending(ctx, job.ID)
	if err != nil || spending.ModelRequests != 5 {
		t.Fatalf("overlap spending = %#v err=%v", spending, err)
	}
}

func TestAgentModelAttemptReservationNeverAcceptsLimitAboveFive(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "attempt-limit", ConversationType: "private", ConversationID: "attempt-limit"}
	runID, err := s.RecordAgentRun(ctx, ident, AgentRun{RawText: "limit"})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < agentModelAttemptLimit; i++ {
		reserved, err := s.ReserveAgentModelAttempt(ctx, runID, 0, agentModelAttemptLimit+10)
		if err != nil || !reserved {
			t.Fatalf("reserve attempt %d: reserved=%v err=%v", i, reserved, err)
		}
	}
	if reserved, err := s.ReserveAgentModelAttempt(ctx, runID, 0, agentModelAttemptLimit+10); err != nil || reserved {
		t.Fatalf("reservation above hard limit: reserved=%v err=%v", reserved, err)
	}
}
