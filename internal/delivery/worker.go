package delivery

import (
	"context"
	"log"
	"time"
)

const (
	defaultPollInterval = time.Second
	defaultBatchSize    = 20
	defaultMaxAttempts  = 5
	staleAttemptAge     = 5 * time.Minute
	staleRecoveryPeriod = 30 * time.Second
)

type Worker struct {
	Service        *Service
	Interval       time.Duration
	BatchSize      int
	MaxAttempts    int
	Now            func() time.Time
	Logger         *log.Logger
	nextRecoveryAt time.Time
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.Service == nil || w.Service.repository == nil {
		return
	}
	w.tick(ctx)
	interval := w.Interval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	if w == nil || w.Service == nil || w.Service.repository == nil {
		return
	}
	now := w.now()
	if w.nextRecoveryAt.IsZero() || !now.Before(w.nextRecoveryAt) {
		w.nextRecoveryAt = now.Add(staleRecoveryPeriod)
		if err := w.Service.repository.RecoverStale(ctx, now.Add(-staleAttemptAge)); err != nil {
			w.logf("recover stale deliveries failed: %v", err)
		}
	}
	if err := w.Service.repository.ExpireDue(ctx, now); err != nil {
		w.logf("expire deliveries failed: %v", err)
		return
	}
	batchSize := w.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	records, err := w.Service.repository.ClaimDue(ctx, now, batchSize)
	if err != nil {
		w.logf("claim deliveries failed: %v", err)
		return
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		outcome := w.Service.DeliverNow(ctx, record.Message)
		maxAttempts := w.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = defaultMaxAttempts
		}
		if outcome.State == OutcomeRetryable && record.Attempts >= maxAttempts {
			outcome.State = OutcomeRejected
			outcome.Code = "attempts_exhausted"
		}
		nextAttempt := time.Time{}
		if outcome.State == OutcomeRetryable {
			nextAttempt = now.Add(retryDelay(record.Attempts))
		}
		if err := w.Service.repository.Complete(ctx, record.ID, outcome, nextAttempt); err != nil {
			w.logf("complete delivery %d failed: %v", record.ID, err)
		}
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Second
	for range attempt - 1 {
		if delay >= 10*time.Minute {
			return 10 * time.Minute
		}
		delay *= 2
	}
	if delay > 10*time.Minute {
		return 10 * time.Minute
	}
	return delay
}

func (w *Worker) now() time.Time {
	if w != nil && w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) logf(format string, args ...any) {
	if w != nil && w.Logger != nil {
		w.Logger.Printf(format, args...)
	}
}
