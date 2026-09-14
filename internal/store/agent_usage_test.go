package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAgentModelAttemptRecordingSurvivesInterruptedRun(t *testing.T) {
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
	const firstAttempts = 70
	for i := 0; i < firstAttempts; i++ {
		if err := s.RecordAgentModelAttempt(ctx, first); err != nil {
			t.Fatalf("record attempt %d: %v", i, err)
		}
	}
	if interrupted, err := s.InterruptStartedAgentRuns(ctx); err != nil || interrupted != 1 {
		t.Fatalf("interrupt started run: interrupted=%d err=%v", interrupted, err)
	}

	second, err := s.RecordAgentRun(ctx, ident, AgentRun{JobID: job.ID, RawText: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentModelAttempt(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAgentRun(ctx, second, AgentRunStatusFailed, "", nil, AgentSpending{}); err != nil {
		t.Fatal(err)
	}
	spending, err := s.AgentJobSpending(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spending.ModelRequests != firstAttempts+1 {
		t.Fatalf("job spending after crash/resume = %#v, want %d attempts", spending, firstAttempts+1)
	}
}

func TestAgentModelAttemptRecordingIsAtomicAcrossWorkers(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "overlap", ConversationType: "private", ConversationID: "overlap"}
	runID, err := s.RecordAgentRun(ctx, ident, AgentRun{RawText: "overlap"})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.RecordAgentModelAttempt(ctx, runID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	spending, err := s.UserSpending(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if spending.ModelRequests != workers {
		t.Fatalf("concurrent attempt count = %d, want %d", spending.ModelRequests, workers)
	}
}

func TestAgentModelAttemptRecordingRejectsFinishedRun(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "finished", ConversationType: "private", ConversationID: "finished"}
	runID, err := s.RecordAgentRun(ctx, ident, AgentRun{RawText: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAgentRun(ctx, runID, AgentRunStatusCompleted, "done", nil, AgentSpending{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentModelAttempt(ctx, runID); err == nil || err.Error() != "agent run is no longer active" {
		t.Fatalf("finished run attempt error = %v", err)
	}
}
