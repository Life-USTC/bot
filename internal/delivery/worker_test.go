package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/message"
)

type workerRepository struct {
	records      []Record
	completed    []Outcome
	nextAttempts []time.Time
	recoveredAt  time.Time
	prunedAt     time.Time
	expiredAt    time.Time
}

func (r *workerRepository) Enqueue(context.Context, message.Outbound) (Record, bool, error) {
	return Record{}, false, nil
}

func (r *workerRepository) ClaimDue(context.Context, time.Time, int) ([]Record, error) {
	records := r.records
	r.records = nil
	return records, nil
}

func (r *workerRepository) Complete(_ context.Context, _ int64, outcome Outcome, next time.Time) error {
	r.completed = append(r.completed, outcome)
	r.nextAttempts = append(r.nextAttempts, next)
	return nil
}

func (r *workerRepository) ExpireDue(_ context.Context, now time.Time) error {
	r.expiredAt = now
	return nil
}

func (r *workerRepository) RecoverStale(_ context.Context, before time.Time) error {
	r.recoveredAt = before
	return nil
}

func (r *workerRepository) PruneOutgoingMessages(_ context.Context, now time.Time) error {
	r.prunedAt = now
	return nil
}

func TestWorkerPersistsRetrySchedule(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	repository := &workerRepository{records: []Record{{
		ID:       1,
		Attempts: 2,
		Message: message.Outbound{
			Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
			Content: message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
		},
	}}}
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeRetryable, Code: "offline"}}
	service, err := New(repository, adapter)
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Service: service, Now: func() time.Time { return now }}
	worker.tick(context.Background())
	if len(repository.completed) != 1 || repository.completed[0].State != OutcomeRetryable {
		t.Fatalf("completed = %#v", repository.completed)
	}
	if want := now.Add(10 * time.Second); !repository.nextAttempts[0].Equal(want) {
		t.Fatalf("next attempt = %v, want %v", repository.nextAttempts[0], want)
	}
}

func TestWorkerRecoversStaleDeliveriesDuringLongLivedRun(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	repository := &workerRepository{}
	service, err := New(repository, &testAdapter{platform: "qqbot"})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Service: service, Now: func() time.Time { return now }}
	worker.tick(t.Context())
	first := repository.recoveredAt
	if want := now.Add(-staleAttemptAge); !first.Equal(want) {
		t.Fatalf("first recovery=%v want=%v", first, want)
	}
	now = now.Add(staleRecoveryPeriod / 2)
	worker.tick(t.Context())
	if !repository.recoveredAt.Equal(first) {
		t.Fatalf("recovery ran before cadence: first=%v got=%v", first, repository.recoveredAt)
	}
	now = now.Add(staleRecoveryPeriod)
	worker.tick(t.Context())
	if want := now.Add(-staleAttemptAge); !repository.recoveredAt.Equal(want) {
		t.Fatalf("periodic recovery=%v want=%v", repository.recoveredAt, want)
	}
}

func TestWorkerPrunesOnStartupAndAtHourlyCadence(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	repository := &workerRepository{}
	service, err := New(repository, &testAdapter{platform: "qqbot"})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Service: service, Now: func() time.Time { return now }}
	worker.tick(t.Context())
	first := repository.prunedAt
	if !first.Equal(now) {
		t.Fatalf("startup prune=%v want=%v", first, now)
	}
	now = now.Add(prunePeriod / 2)
	worker.tick(t.Context())
	if !repository.prunedAt.Equal(first) {
		t.Fatalf("prune ran before cadence: first=%v got=%v", first, repository.prunedAt)
	}
	now = now.Add(prunePeriod / 2)
	worker.tick(t.Context())
	if !repository.prunedAt.Equal(now) {
		t.Fatalf("periodic prune=%v want=%v", repository.prunedAt, now)
	}
}

func TestWorkerStopsRetryingAfterAttemptBudget(t *testing.T) {
	repository := &workerRepository{records: []Record{{
		ID:       1,
		Attempts: defaultMaxAttempts,
		Message: message.Outbound{
			Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
			Content: message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
		},
	}}}
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeRetryable}}
	service, err := New(repository, adapter)
	if err != nil {
		t.Fatal(err)
	}
	(&Worker{Service: service}).tick(context.Background())
	if got := repository.completed[0]; got.State != OutcomeRejected || got.Code != "attempts_exhausted" {
		t.Fatalf("outcome = %#v", got)
	}
	if !repository.nextAttempts[0].IsZero() {
		t.Fatalf("unexpected retry = %v", repository.nextAttempts[0])
	}
}
