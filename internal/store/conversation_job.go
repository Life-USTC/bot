package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConversationJobState is the durable state of one user interaction. Waiting
// states do not hold a worker lease; a conversation event or a poller moves
// them back to queued when the wait condition is satisfied.
type ConversationJobState string

const (
	ConversationJobStateQueued              ConversationJobState = "queued"
	ConversationJobStateRunning             ConversationJobState = "running"
	ConversationJobStateWaitingAuth         ConversationJobState = "waiting_auth"
	ConversationJobStateWaitingConfirmation ConversationJobState = "waiting_confirmation"
	ConversationJobStateWaitingInput        ConversationJobState = "waiting_input"
	ConversationJobStateWaitingDelivery     ConversationJobState = "waiting_delivery"
	ConversationJobStateDeliveryUnknown     ConversationJobState = "delivery_unknown"
	ConversationJobStateRetryWait           ConversationJobState = "retry_wait"
	ConversationJobStateCompleted           ConversationJobState = "completed"
	ConversationJobStateFailed              ConversationJobState = "failed"
	ConversationJobStateExpired             ConversationJobState = "expired"
	ConversationJobStateCancelled           ConversationJobState = "cancelled"
)

// ConversationJobWaitReason explains why a job is in one of its waiting
// states. It is intentionally narrower than the state set so callers cannot
// accidentally encode a second, conflicting state machine in the database.
type ConversationJobWaitReason string

const (
	ConversationJobWaitReasonNone         ConversationJobWaitReason = ""
	ConversationJobWaitReasonAuth         ConversationJobWaitReason = "auth"
	ConversationJobWaitReasonConfirmation ConversationJobWaitReason = "confirmation"
	ConversationJobWaitReasonInput        ConversationJobWaitReason = "input"
	ConversationJobWaitReasonDelivery     ConversationJobWaitReason = "delivery"
)

// ConversationJobInput is the typed, resumable input envelope stored in a
// conversation job. Data is optional and lets a caller retain structured
// input without introducing another persistence table.
type ConversationJobInput struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ConversationJobInvocation is the typed invocation envelope persisted with a
// job. Data is available for capability-specific arguments while Name,
// Command, and Args cover the common command and agent paths.
type ConversationJobInvocation struct {
	Name    string          `json:"name,omitempty"`
	Command string          `json:"command,omitempty"`
	Args    []string        `json:"args,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ConversationJobEnqueue describes a new durable interaction. InputJSON and
// InvocationJSON may be used when the caller already owns a JSON envelope;
// when omitted, the typed Input and Invocation values are encoded instead.
// A source event ID is required because enqueue is idempotent on that event.
type ConversationJobEnqueue struct {
	Identity       Identity
	SourceEventID  string
	Input          ConversationJobInput
	InputJSON      string
	Invocation     ConversationJobInvocation
	InvocationJSON string
	State          ConversationJobState
	WaitReason     ConversationJobWaitReason
	RetryAt        time.Time
	ExpiresAt      time.Time
	MaxAttempts    int
}

// ConversationJob is the store-facing representation of one interaction.
// InputJSON and InvocationJSON expose the exact persisted envelopes in
// addition to their common typed forms.
type ConversationJob struct {
	ID             int64
	Identity       Identity
	Sequence       int64
	SourceEventID  string
	State          ConversationJobState
	WaitReason     ConversationJobWaitReason
	Input          ConversationJobInput
	InputJSON      string
	Invocation     ConversationJobInvocation
	InvocationJSON string
	LeaseToken     string
	ClaimedAt      *time.Time
	Revision       int
	Attempts       int
	MaxAttempts    int
	RetryAt        time.Time
	ExpiresAt      time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ConversationJobTransition is applied with the lease token returned by a
// successful claim. It is a compare-and-swap: a stale token, a terminal row,
// or a row no longer in running state leaves the row untouched and returns
// false.
type ConversationJobTransition struct {
	State          ConversationJobState
	WaitReason     ConversationJobWaitReason
	Input          ConversationJobInput
	InputJSON      string
	Invocation     ConversationJobInvocation
	InvocationJSON string
	RetryAt        time.Time
	LastError      string
}

const (
	// ConversationJobTTL bounds an interaction that is waiting for a user or
	// an external system. Callers can provide a shorter or longer expiry per
	// enqueue request.
	ConversationJobTTL = 15 * time.Minute
	// ConversationJobLease is the maximum time a worker may hold a running
	// claim before a recovery pass makes it retryable.
	ConversationJobLease = 2 * time.Minute
)

var conversationJobBlockingStates = []ConversationJobState{
	ConversationJobStateQueued,
	ConversationJobStateRunning,
	ConversationJobStateWaitingAuth,
	ConversationJobStateWaitingConfirmation,
	ConversationJobStateWaitingInput,
	ConversationJobStateWaitingDelivery,
	ConversationJobStateDeliveryUnknown,
	ConversationJobStateRetryWait,
}

var conversationJobClaimableStates = []ConversationJobState{
	ConversationJobStateQueued,
	ConversationJobStateRetryWait,
}

type conversationJobRow struct {
	ID               int64      `gorm:"primaryKey"`
	UserID           int64      `gorm:"not null;index"`
	Platform         string     `gorm:"not null;uniqueIndex:idx_conversation_jobs_source_event,priority:1;uniqueIndex:idx_conversation_jobs_conversation_sequence,priority:1;index:idx_conversation_jobs_conversation_state,priority:1"`
	ExternalUserID   string     `gorm:"not null"`
	ConversationType string     `gorm:"not null;uniqueIndex:idx_conversation_jobs_conversation_sequence,priority:2;index:idx_conversation_jobs_conversation_state,priority:2"`
	ConversationID   string     `gorm:"not null;uniqueIndex:idx_conversation_jobs_conversation_sequence,priority:3;index:idx_conversation_jobs_conversation_state,priority:3"`
	Sequence         int64      `gorm:"not null;uniqueIndex:idx_conversation_jobs_conversation_sequence,priority:4;index"`
	SourceEventID    string     `gorm:"not null;uniqueIndex:idx_conversation_jobs_source_event,priority:2"`
	State            string     `gorm:"not null;index:idx_conversation_jobs_state_expiry,priority:1;index:idx_conversation_jobs_conversation_state,priority:4"`
	WaitReason       string     `gorm:"not null;default:''"`
	InputJSON        string     `gorm:"not null"`
	InvocationJSON   string     `gorm:"not null"`
	LeaseToken       string     `gorm:"index"`
	ClaimedAt        *time.Time `gorm:"index"`
	Revision         int        `gorm:"not null;default:1"`
	Attempts         int        `gorm:"not null;default:0"`
	MaxAttempts      int        `gorm:"not null;default:0"`
	RetryAt          *time.Time `gorm:"index:idx_conversation_jobs_state_expiry,priority:2"`
	ExpiresAt        *time.Time `gorm:"index:idx_conversation_jobs_state_expiry,priority:3"`
	LastError        string
	CreatedAt        time.Time `gorm:"index"`
	UpdatedAt        time.Time
}

func (conversationJobRow) TableName() string {
	return "conversation_jobs"
}

// A separate counter row lets enqueue assign a sequence without relying on a
// racy MAX(sequence) read. The enqueue transaction touches the row before it
// checks the source-event key, so duplicate concurrent enqueues do not consume
// a sequence number after the first transaction commits.
type conversationJobSequenceRow struct {
	ID               int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null;uniqueIndex:idx_conversation_job_sequences_identity,priority:1"`
	ConversationType string `gorm:"not null;uniqueIndex:idx_conversation_job_sequences_identity,priority:2"`
	ConversationID   string `gorm:"not null;uniqueIndex:idx_conversation_job_sequences_identity,priority:3"`
	NextSequence     int64  `gorm:"not null;default:0"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (conversationJobSequenceRow) TableName() string {
	return "conversation_job_sequences"
}

// EnqueueConversationJob inserts a job once for its source event. The bool is
// true only when a row was inserted; a duplicate returns the original row.
func (s *Store) EnqueueConversationJob(ctx context.Context, input ConversationJobEnqueue) (ConversationJob, bool, error) {
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	if err := validateConversationIdentity(input.Identity); err != nil {
		return ConversationJob{}, false, err
	}
	input.Identity = normalizeIdentity(input.Identity)
	input.SourceEventID = strings.TrimSpace(input.SourceEventID)
	if input.SourceEventID == "" {
		return ConversationJob{}, false, errors.New("conversation job source event id is empty")
	}
	if input.MaxAttempts < 0 {
		return ConversationJob{}, false, errors.New("conversation job max attempts is invalid")
	}
	now := nowUTC()
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = now.Add(ConversationJobTTL)
	}
	input.ExpiresAt = input.ExpiresAt.UTC()
	if !input.ExpiresAt.After(now) {
		return ConversationJob{}, false, errors.New("conversation job expiry must be in the future")
	}
	if input.RetryAt.IsZero() {
		input.RetryAt = time.Time{}
	} else {
		input.RetryAt = input.RetryAt.UTC()
	}
	if input.State == "" {
		input.State = ConversationJobStateQueued
	}
	switch input.State {
	case ConversationJobStateQueued,
		ConversationJobStateWaitingAuth,
		ConversationJobStateWaitingConfirmation,
		ConversationJobStateWaitingInput,
		ConversationJobStateWaitingDelivery,
		ConversationJobStateRetryWait:
		// These are the only states a new job can enter without a worker
		// lease. Running and terminal states must be reached through a CAS.
	default:
		return ConversationJob{}, false, fmt.Errorf("conversation job cannot be enqueued in state %q", input.State)
	}
	waitReason, err := normalizeConversationJobWait(input.State, input.WaitReason)
	if err != nil {
		return ConversationJob{}, false, err
	}
	if input.State == ConversationJobStateRetryWait && input.RetryAt.IsZero() {
		input.RetryAt = now
	}
	if input.State != ConversationJobStateRetryWait && !input.RetryAt.IsZero() {
		return ConversationJob{}, false, errors.New("conversation job retry time requires retry_wait state")
	}
	inputJSON, err := normalizeConversationJobJSON(input.InputJSON, input.Input, "input")
	if err != nil {
		return ConversationJob{}, false, err
	}
	invocationJSON, err := normalizeConversationJobJSON(input.InvocationJSON, input.Invocation, "invocation")
	if err != nil {
		return ConversationJob{}, false, err
	}

	var saved conversationJobRow
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		userID, err := ensureUser(tx, input.Identity, now)
		if err != nil {
			return err
		}
		sequenceRow := conversationJobSequenceRow{
			Platform:         input.Identity.Platform,
			ConversationType: input.Identity.ConversationType,
			ConversationID:   input.Identity.ConversationID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&sequenceRow).Error; err != nil {
			return err
		}
		// This matched update is deliberately before the dedupe lookup. SQLite
		// serializes the writer at this point, which makes the sequence counter
		// and source-event check one atomic critical section.
		if err := tx.Model(&conversationJobSequenceRow{}).
			Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
				input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID).
			Update("updated_at", now).Error; err != nil {
			return err
		}
		if err := tx.Where("platform = ? AND source_event_id = ?", input.Identity.Platform, input.SourceEventID).
			First(&saved).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if err := tx.Model(&conversationJobSequenceRow{}).
			Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
				input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID).
			Updates(map[string]any{
				"next_sequence": gorm.Expr("next_sequence + 1"),
				"updated_at":    now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
			input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID).
			First(&sequenceRow).Error; err != nil {
			return err
		}
		expiresAt := input.ExpiresAt
		row := conversationJobRow{
			UserID:           userID,
			Platform:         input.Identity.Platform,
			ExternalUserID:   input.Identity.UserID,
			ConversationType: input.Identity.ConversationType,
			ConversationID:   input.Identity.ConversationID,
			Sequence:         sequenceRow.NextSequence,
			SourceEventID:    input.SourceEventID,
			State:            string(input.State),
			WaitReason:       string(waitReason),
			InputJSON:        inputJSON,
			InvocationJSON:   invocationJSON,
			Revision:         1,
			Attempts:         0,
			MaxAttempts:      input.MaxAttempts,
			RetryAt:          conversationJobTimePtr(input.RetryAt),
			ExpiresAt:        &expiresAt,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.Where("platform = ? AND source_event_id = ?", input.Identity.Platform, input.SourceEventID).
				First(&saved).Error; err != nil {
				return err
			}
			return nil
		}
		saved = row
		created = true
		return nil
	})
	if err != nil {
		return ConversationJob{}, false, err
	}
	job, err := conversationJobFromRow(saved)
	if err != nil {
		return ConversationJob{}, false, err
	}
	return job, created, nil
}

// GetConversationJob returns one job by ID. A missing job is represented by a
// nil pointer, matching the store's other active-record lookups.
func (s *Store) GetConversationJob(ctx context.Context, id int64) (*ConversationJob, error) {
	if id <= 0 {
		return nil, errors.New("conversation job id is invalid")
	}
	var row conversationJobRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	job, err := conversationJobFromRow(row)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ClaimConversationJob claims the oldest eligible job for one actor's lane in
// a conversation. The optional timestamp exists for deterministic callers and
// tests; omitted calls use the store clock.
func (s *Store) ClaimConversationJob(ctx context.Context, ident Identity, at ...time.Time) (*ConversationJob, error) {
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	ident = normalizeIdentity(ident)
	now := nowUTC()
	if len(at) > 0 && !at[0].IsZero() {
		now = at[0].UTC()
	}
	var claimed *ConversationJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row conversationJobRow
		query := eligibleConversationJobs(tx, now).
			Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND external_user_id = ?",
				ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID).
			Order("sequence ASC").Limit(1)
		if err := query.First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		job, err := claimConversationJobRow(tx, row.ID, now)
		if err != nil {
			return err
		}
		claimed = job
		return nil
	})
	return claimed, err
}

// ClaimNextConversationJob claims the oldest eligible job across all
// conversations. It is the one-job form of ClaimConversationJobs.
func (s *Store) ClaimNextConversationJob(ctx context.Context, at ...time.Time) (*ConversationJob, error) {
	jobs, err := s.ClaimConversationJobs(ctx, claimConversationJobTime(at), 1)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

// ClaimConversationJobs atomically claims up to limit FIFO-eligible jobs. At
// most one job at a time can be claimed from an actor's conversation lane.
func (s *Store) ClaimConversationJobs(ctx context.Context, now time.Time, limit int) ([]ConversationJob, error) {
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	if limit <= 0 {
		return nil, nil
	}
	now = normalizeStoreTime(now)
	claimed := make([]ConversationJob, 0, limit)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []conversationJobRow
		if err := eligibleConversationJobs(tx, now).Order("sequence ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			job, err := claimConversationJobRow(tx, row.ID, now)
			if err != nil {
				return err
			}
			if job == nil {
				continue
			}
			claimed = append(claimed, *job)
		}
		return nil
	})
	return claimed, err
}

func claimConversationJobTime(at []time.Time) time.Time {
	if len(at) > 0 && !at[0].IsZero() {
		return at[0].UTC()
	}
	return nowUTC()
}

func eligibleConversationJobs(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Model(&conversationJobRow{}).
		Where("state IN ?", conversationJobStateStrings(conversationJobClaimableStates)).
		Where("(retry_at IS NULL OR retry_at <= ?)", now).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Where("(max_attempts <= 0 OR attempts < max_attempts)").
		Where(`NOT EXISTS (
			SELECT 1 FROM conversation_jobs earlier
			WHERE earlier.platform = conversation_jobs.platform
			  AND earlier.conversation_type = conversation_jobs.conversation_type
			  AND earlier.conversation_id = conversation_jobs.conversation_id
			  AND earlier.external_user_id = conversation_jobs.external_user_id
			  AND earlier.sequence < conversation_jobs.sequence
			  AND earlier.state IN ?
		)`, conversationJobStateStrings(conversationJobBlockingStates))
}

func claimConversationJobRow(tx *gorm.DB, id int64, now time.Time) (*ConversationJob, error) {
	token, err := conversationJobLeaseToken()
	if err != nil {
		return nil, err
	}
	claimedAt := now.UTC()
	result := tx.Model(&conversationJobRow{}).
		Where("id = ?", id).
		Where("state IN ?", conversationJobStateStrings(conversationJobClaimableStates)).
		Where("(retry_at IS NULL OR retry_at <= ?)", now).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Where("(max_attempts <= 0 OR attempts < max_attempts)").
		Where(`NOT EXISTS (
			SELECT 1 FROM conversation_jobs earlier
			WHERE earlier.platform = conversation_jobs.platform
			  AND earlier.conversation_type = conversation_jobs.conversation_type
			  AND earlier.conversation_id = conversation_jobs.conversation_id
			  AND earlier.external_user_id = conversation_jobs.external_user_id
			  AND earlier.sequence < conversation_jobs.sequence
			  AND earlier.state IN ?
		)`, conversationJobStateStrings(conversationJobBlockingStates)).
		Updates(map[string]any{
			"state":       string(ConversationJobStateRunning),
			"wait_reason": "",
			"attempts":    gorm.Expr("attempts + 1"),
			"lease_token": token,
			"claimed_at":  claimedAt,
			"updated_at":  now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var row conversationJobRow
	if err := tx.Where("id = ? AND lease_token = ?", id, token).First(&row).Error; err != nil {
		return nil, err
	}
	job, err := conversationJobFromRow(row)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// TransitionConversationJob performs a lease-token CAS from running to an
// explicit next state. It returns false when another worker already resolved
// the claim or when the row is terminal.
func (s *Store) TransitionConversationJob(ctx context.Context, id int64, leaseToken string, transition ConversationJobTransition) (bool, error) {
	if id <= 0 {
		return false, errors.New("conversation job id is invalid")
	}
	leaseToken = strings.TrimSpace(leaseToken)
	if leaseToken == "" {
		return false, errors.New("conversation job lease token is empty")
	}
	if err := validateConversationJobTransition(transition); err != nil {
		return false, err
	}
	updates, err := conversationJobTransitionUpdates(transition)
	if err != nil {
		return false, err
	}
	updates["updated_at"] = nowUTC()
	result := s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("id = ? AND state = ? AND lease_token = ?", id, string(ConversationJobStateRunning), leaseToken).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// CompleteConversationJob resolves a running claim successfully.
func (s *Store) CompleteConversationJob(ctx context.Context, id int64, leaseToken string) (bool, error) {
	return s.TransitionConversationJob(ctx, id, leaseToken, ConversationJobTransition{State: ConversationJobStateCompleted})
}

// FailConversationJob resolves a running claim permanently with an error.
func (s *Store) FailConversationJob(ctx context.Context, id int64, leaseToken, reason string) (bool, error) {
	return s.TransitionConversationJob(ctx, id, leaseToken, ConversationJobTransition{
		State:     ConversationJobStateFailed,
		LastError: trimConversationJobError(reason),
	})
}

// CancelConversationJob cancels any non-terminal job, including one waiting on
// an external condition. A running worker that races with cancellation loses
// its lease-token CAS when it later tries to resolve the row.
func (s *Store) CancelConversationJob(ctx context.Context, id int64, reason string) (bool, error) {
	if id <= 0 {
		return false, errors.New("conversation job id is invalid")
	}
	result := s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("id = ? AND state IN ?", id, conversationJobStateStrings(conversationJobBlockingStates)).
		Updates(map[string]any{
			"state":       string(ConversationJobStateCancelled),
			"wait_reason": "",
			"lease_token": "",
			"claimed_at":  nil,
			"retry_at":    nil,
			"last_error":  trimConversationJobError(reason),
			"updated_at":  nowUTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func validateConversationJobTransition(transition ConversationJobTransition) error {
	if !validConversationJobState(transition.State) {
		return fmt.Errorf("invalid conversation job transition state %q", transition.State)
	}
	if transition.State == ConversationJobStateRunning {
		return errors.New("conversation job cannot transition to running directly")
	}
	_, err := normalizeConversationJobWait(transition.State, transition.WaitReason)
	if err != nil {
		return err
	}
	return nil
}

func conversationJobTransitionUpdates(transition ConversationJobTransition) (map[string]any, error) {
	waitReason, err := normalizeConversationJobWait(transition.State, transition.WaitReason)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"state":       string(transition.State),
		"wait_reason": string(waitReason),
		"lease_token": "",
		"claimed_at":  nil,
		"retry_at":    nil,
		"last_error":  trimConversationJobError(transition.LastError),
	}
	if transition.State == ConversationJobStateRetryWait {
		retryAt := transition.RetryAt
		if retryAt.IsZero() {
			retryAt = nowUTC()
		}
		updates["retry_at"] = retryAt.UTC()
	}
	if transition.State == ConversationJobStateDeliveryUnknown && strings.TrimSpace(transition.LastError) == "" {
		updates["last_error"] = "delivery outcome is unknown"
	}
	if transition.InputJSON != "" || !conversationJobInputZero(transition.Input) {
		inputJSON, err := normalizeConversationJobJSON(transition.InputJSON, transition.Input, "input")
		if err != nil {
			return nil, err
		}
		updates["input_json"] = inputJSON
	}
	if transition.InvocationJSON != "" || !conversationJobInvocationZero(transition.Invocation) {
		invocationJSON, err := normalizeConversationJobJSON(transition.InvocationJSON, transition.Invocation, "invocation")
		if err != nil {
			return nil, err
		}
		updates["invocation_json"] = invocationJSON
	}
	return updates, nil
}

// ResolveUnknownConversationJob is the manual CAS for a delivery_unknown
// result. Automatic workers never reclaim that state because the platform may
// already have accepted the message.
func (s *Store) ResolveUnknownConversationJob(ctx context.Context, id int64, state ConversationJobState, reason string) (bool, error) {
	if id <= 0 {
		return false, errors.New("conversation job id is invalid")
	}
	if state != ConversationJobStateCompleted && state != ConversationJobStateCancelled && state != ConversationJobStateFailed {
		return false, errors.New("unknown delivery can only resolve to a terminal state")
	}
	updates := map[string]any{
		"state":       string(state),
		"wait_reason": "",
		"lease_token": "",
		"claimed_at":  nil,
		"retry_at":    nil,
		"last_error":  trimConversationJobError(reason),
		"updated_at":  nowUTC(),
	}
	result := s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("id = ? AND state = ?", id, string(ConversationJobStateDeliveryUnknown)).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// UnblockConversationJobsAfterAuth releases every unexpired auth-waiting job
// for one actor's conversation lane. FIFO still serializes subsequent claims.
func (s *Store) UnblockConversationJobsAfterAuth(ctx context.Context, ident Identity, at ...time.Time) error {
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
	ident = normalizeIdentity(ident)
	now := claimConversationJobTime(at)
	return s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND external_user_id = ? AND state = ? AND expires_at > ?",
			ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID, string(ConversationJobStateWaitingAuth), now).
		Updates(map[string]any{
			"state":       string(ConversationJobStateQueued),
			"wait_reason": "",
			"retry_at":    nil,
			"revision":    gorm.Expr("revision + 1"),
			"updated_at":  now,
		}).Error
}

// UnblockAuthorizedConversationJobs repairs the small crash window between a
// successful credential commit and the auth poller's wake-up call. A pending
// login session deliberately blocks an older credential from releasing work.
func (s *Store) UnblockAuthorizedConversationJobs(ctx context.Context, now time.Time) error {
	now = normalizeStoreTime(now)
	return s.db.WithContext(ctx).Exec(`
		UPDATE conversation_jobs
		SET state = ?, wait_reason = '', retry_at = NULL, revision = revision + 1, updated_at = ?
		WHERE state = ?
		  AND (expires_at IS NULL OR expires_at > ?)
		  AND EXISTS (
			SELECT 1 FROM credentials
			WHERE credentials.user_id = conversation_jobs.user_id
			  AND credentials.expires_at > ?
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM login_sessions
			WHERE login_sessions.user_id = conversation_jobs.user_id
			  AND login_sessions.platform = conversation_jobs.platform
			  AND login_sessions.conversation_type = conversation_jobs.conversation_type
			  AND login_sessions.conversation_id = conversation_jobs.conversation_id
			  AND login_sessions.status = ?
		  )`,
		string(ConversationJobStateQueued), now,
		string(ConversationJobStateWaitingAuth), now, now,
		string(LoginStatusPending),
	).Error
}

// ResumeConversationJobInput stores a user-provided input and queues a job
// that was waiting for that input. The waiting state is the CAS predicate, so
// duplicate replies consume the wait exactly once.
func (s *Store) ResumeConversationJobInput(ctx context.Context, id int64, input ConversationJobInput, at ...time.Time) (bool, error) {
	if id <= 0 {
		return false, errors.New("conversation job id is invalid")
	}
	inputJSON, err := normalizeConversationJobJSON("", input, "input")
	if err != nil {
		return false, err
	}
	now := claimConversationJobTime(at)
	result := s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("id = ? AND state = ? AND expires_at > ?", id, string(ConversationJobStateWaitingInput), now).
		Updates(map[string]any{
			"state":       string(ConversationJobStateQueued),
			"wait_reason": "",
			"input_json":  inputJSON,
			"retry_at":    nil,
			"revision":    gorm.Expr("revision + 1"),
			"updated_at":  now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RecoverConversationJobLeases moves stale running jobs into retry_wait. The
// optional lease duration is useful when a deployment has a different worker
// heartbeat; omitted calls use ConversationJobLease.
func (s *Store) RecoverConversationJobLeases(ctx context.Context, now time.Time, lease ...time.Duration) error {
	now = normalizeStoreTime(now)
	leaseFor := ConversationJobLease
	if len(lease) > 0 && lease[0] > 0 {
		leaseFor = lease[0]
	}
	cutoff := now.Add(-leaseFor)
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&conversationJobRow{}).
			Where("state = ? AND claimed_at IS NOT NULL AND claimed_at <= ? AND expires_at > ?",
				string(ConversationJobStateRunning), cutoff, now).
			Updates(map[string]any{
				"state":       string(ConversationJobStateRetryWait),
				"wait_reason": "",
				"lease_token": "",
				"claimed_at":  nil,
				"retry_at":    now,
				"last_error":  "worker lease expired before the job was resolved",
				"updated_at":  now,
			}).Error; err != nil {
			return err
		}
		// A running capability may already have reached an external service when
		// its worker died. Never retry it silently: record the honest unknown
		// outcome before the owning job can be reclaimed.
		return tx.Model(&capabilityExecutionRow{}).
			Where("state = ? AND job_id IN (SELECT id FROM conversation_jobs WHERE state <> ?)",
				string(CapabilityExecutionRunning), string(ConversationJobStateRunning)).
			Updates(map[string]any{
				"state":       string(CapabilityExecutionUnknown),
				"error":       "worker stopped after capability execution began; external outcome is unknown",
				"finished_at": now,
				"updated_at":  now,
			}).Error
	})
}

// ExpireConversationJobs marks every unexpired-state job whose TTL elapsed as
// expired. Terminal states are excluded, so expiry cannot rewrite a resolved
// result.
func (s *Store) ExpireConversationJobs(ctx context.Context, now time.Time) error {
	now = normalizeStoreTime(now)
	return s.db.WithContext(ctx).Model(&conversationJobRow{}).
		Where("state IN ? AND expires_at IS NOT NULL AND expires_at <= ?",
			conversationJobStateStrings(conversationJobBlockingStates), now).
		Updates(map[string]any{
			"state":       string(ConversationJobStateExpired),
			"wait_reason": "",
			"lease_token": "",
			"claimed_at":  nil,
			"retry_at":    nil,
			"last_error":  "conversation job expired",
			"updated_at":  now,
		}).Error
}

func normalizeConversationJobWait(state ConversationJobState, reason ConversationJobWaitReason) (ConversationJobWaitReason, error) {
	if !validConversationJobState(state) {
		return "", fmt.Errorf("invalid conversation job state %q", state)
	}
	want := ConversationJobWaitReasonNone
	switch state {
	case ConversationJobStateWaitingAuth:
		want = ConversationJobWaitReasonAuth
	case ConversationJobStateWaitingConfirmation:
		want = ConversationJobWaitReasonConfirmation
	case ConversationJobStateWaitingInput:
		want = ConversationJobWaitReasonInput
	case ConversationJobStateWaitingDelivery:
		want = ConversationJobWaitReasonDelivery
	}
	if reason == "" {
		reason = want
	}
	if reason != want {
		return "", fmt.Errorf("conversation job state %q requires wait reason %q", state, want)
	}
	return reason, nil
}

func validConversationJobState(state ConversationJobState) bool {
	switch state {
	case ConversationJobStateQueued,
		ConversationJobStateRunning,
		ConversationJobStateWaitingAuth,
		ConversationJobStateWaitingConfirmation,
		ConversationJobStateWaitingInput,
		ConversationJobStateWaitingDelivery,
		ConversationJobStateDeliveryUnknown,
		ConversationJobStateRetryWait,
		ConversationJobStateCompleted,
		ConversationJobStateFailed,
		ConversationJobStateExpired,
		ConversationJobStateCancelled:
		return true
	default:
		return false
	}
}

func conversationJobStateStrings(states []ConversationJobState) []string {
	values := make([]string, 0, len(states))
	for _, state := range states {
		values = append(values, string(state))
	}
	return values
}

func conversationJobFromRow(row conversationJobRow) (ConversationJob, error) {
	var input ConversationJobInput
	if err := json.Unmarshal([]byte(row.InputJSON), &input); err != nil {
		return ConversationJob{}, fmt.Errorf("decode conversation job %d input: %w", row.ID, err)
	}
	var invocation ConversationJobInvocation
	if err := json.Unmarshal([]byte(row.InvocationJSON), &invocation); err != nil {
		return ConversationJob{}, fmt.Errorf("decode conversation job %d invocation: %w", row.ID, err)
	}
	job := ConversationJob{
		ID:             row.ID,
		Identity:       Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		Sequence:       row.Sequence,
		SourceEventID:  row.SourceEventID,
		State:          ConversationJobState(row.State),
		WaitReason:     ConversationJobWaitReason(row.WaitReason),
		Input:          input,
		InputJSON:      row.InputJSON,
		Invocation:     invocation,
		InvocationJSON: row.InvocationJSON,
		LeaseToken:     row.LeaseToken,
		ClaimedAt:      conversationJobTimePtrValue(row.ClaimedAt),
		Revision:       row.Revision,
		Attempts:       row.Attempts,
		MaxAttempts:    row.MaxAttempts,
		RetryAt:        conversationJobTimeValue(row.RetryAt),
		ExpiresAt:      conversationJobTimeValue(row.ExpiresAt),
		LastError:      row.LastError,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	return job, nil
}

func normalizeConversationJobJSON(raw string, fallback any, field string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		encoded, err := json.Marshal(fallback)
		if err != nil {
			return "", fmt.Errorf("encode conversation job %s: %w", field, err)
		}
		raw = string(encoded)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return "", fmt.Errorf("conversation job %s is invalid JSON: %w", field, err)
	}
	return compact.String(), nil
}

func conversationJobInputZero(input ConversationJobInput) bool {
	return strings.TrimSpace(input.Text) == "" && len(input.Data) == 0
}

func conversationJobInvocationZero(invocation ConversationJobInvocation) bool {
	return strings.TrimSpace(invocation.Name) == "" &&
		strings.TrimSpace(invocation.Command) == "" &&
		len(invocation.Args) == 0 && len(invocation.Data) == 0
}

func trimConversationJobError(reason string) string {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 1000 {
		return string([]rune(reason)[:1000])
	}
	return reason
}

func conversationJobLeaseToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate conversation job lease token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func conversationJobTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func conversationJobTimeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func conversationJobTimePtrValue(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}
