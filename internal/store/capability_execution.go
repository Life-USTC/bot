package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CapabilityExecutionState string

const (
	CapabilityExecutionAwaitingConfirmation CapabilityExecutionState = "awaiting_confirmation"
	CapabilityExecutionApproved             CapabilityExecutionState = "approved"
	CapabilityExecutionDenied               CapabilityExecutionState = "denied"
	CapabilityExecutionCancelled            CapabilityExecutionState = "cancelled"
	CapabilityExecutionExpired              CapabilityExecutionState = "expired"
	CapabilityExecutionWaitingAuth          CapabilityExecutionState = "waiting_auth"
	CapabilityExecutionRunning              CapabilityExecutionState = "running"
	CapabilityExecutionSucceeded            CapabilityExecutionState = "succeeded"
	CapabilityExecutionFailed               CapabilityExecutionState = "failed"
	CapabilityExecutionUnknown              CapabilityExecutionState = "unknown"
)

type CapabilityReceipt struct {
	Action   string `json:"action,omitempty"`
	Resource string `json:"resource,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

type CapabilityExecution struct {
	ID                   string
	Identity             Identity
	JobID                int64
	Sequence             int
	DedupeKey            string
	ToolCallID           string
	LeaseToken           string
	Capability           string
	Arguments            []string
	Effect               string
	State                CapabilityExecutionState
	Receipt              CapabilityReceipt
	Result               string
	Error                string
	ConfirmationOutboxID int64
	ConfirmationEventID  string
	ConfirmedAt          *time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	ReceiptSentAt        *time.Time
	ReceiptState         CapabilityExecutionState
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CapabilityExecutionPrepare struct {
	Identity Identity
	JobID    int64
	// LeaseToken optionally binds preparation to the caller's running job.
	// A claim is still required before an external effect can be invoked.
	LeaseToken           string
	Sequence             int
	DedupeKey            string
	ToolCallID           string
	Capability           string
	Arguments            []string
	Effect               string
	Receipt              CapabilityReceipt
	RequiresConfirmation bool
}

type CapabilityConfirmationDecision struct {
	Approved      bool
	Reason        string
	SourceEventID string
}

type capabilityExecutionRow struct {
	ID                   string `gorm:"primaryKey"`
	UserID               int64  `gorm:"not null;index"`
	Platform             string `gorm:"not null;uniqueIndex:idx_capability_executions_dedupe,priority:1;uniqueIndex:idx_capability_executions_confirmation_event,priority:1;index:idx_capability_executions_identity_state,priority:1"`
	ExternalUserID       string `gorm:"not null;index:idx_capability_executions_identity_state,priority:4"`
	ConversationType     string `gorm:"not null;index:idx_capability_executions_identity_state,priority:2"`
	ConversationID       string `gorm:"not null;index:idx_capability_executions_identity_state,priority:3"`
	JobID                int64  `gorm:"not null;index:idx_capability_executions_job_sequence,priority:1"`
	Sequence             int    `gorm:"not null;index:idx_capability_executions_job_sequence,priority:2"`
	DedupeKey            string `gorm:"not null;uniqueIndex:idx_capability_executions_dedupe,priority:2"`
	ToolCallID           string `gorm:"not null;default:'';index"`
	LeaseToken           string `gorm:"index"`
	Capability           string `gorm:"not null"`
	ArgumentsJSON        string `gorm:"not null"`
	Effect               string `gorm:"not null"`
	State                string `gorm:"not null;index:idx_capability_executions_identity_state,priority:5"`
	ReceiptJSON          string `gorm:"not null;default:'{}'"`
	Result               string
	Error                string
	ConfirmationOutboxID int64   `gorm:"index"`
	ConfirmationEventID  *string `gorm:"uniqueIndex:idx_capability_executions_confirmation_event,priority:2"`
	ConfirmedAt          *time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	ReceiptSentAt        *time.Time `gorm:"index"`
	ReceiptState         string     `gorm:"not null;default:''"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (capabilityExecutionRow) TableName() string { return "capability_executions" }

func (s *Store) PrepareCapabilityExecution(ctx context.Context, input CapabilityExecutionPrepare) (CapabilityExecution, bool, error) {
	executions, created, err := s.PrepareCapabilityExecutions(ctx, []CapabilityExecutionPrepare{input})
	if err != nil {
		return CapabilityExecution{}, false, err
	}
	if len(executions) != 1 {
		return CapabilityExecution{}, false, errors.New("capability execution batch returned an unexpected number of operations")
	}
	return executions[0], created, nil
}

// PrepareCapabilityExecutions persists a complete operation batch in one
// transaction. Inputs are normalized and validated before the first row is
// written, so a failed preflight or insert cannot leave an orphaned
// confirmation item behind. Existing dedupe rows are returned unchanged.
func (s *Store) PrepareCapabilityExecutions(ctx context.Context, inputs []CapabilityExecutionPrepare) ([]CapabilityExecution, bool, error) {
	if len(inputs) == 0 {
		return nil, false, errors.New("capability execution batch is empty")
	}
	normalized := make([]preparedCapabilityExecution, len(inputs))
	for index, input := range inputs {
		prepared, err := normalizeCapabilityExecutionPrepare(input)
		if err != nil {
			return nil, false, fmt.Errorf("prepare capability execution %d: %w", index, err)
		}
		normalized[index] = prepared
	}

	createdAll := true
	rows := make([]capabilityExecutionRow, len(normalized))
	now := nowUTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		first := normalized[0].input
		for index, prepared := range normalized {
			input := prepared.input
			if input.JobID != first.JobID || input.Identity != first.Identity {
				return errors.New("capability execution batch must belong to one conversation job")
			}
			if input.LeaseToken != "" {
				if err := requireRunningConversationJobLease(tx, input.JobID, input.LeaseToken, input.Identity, now); err != nil {
					return err
				}
			} else if err := requireLiveConversationJob(tx, input.JobID, input.Identity, now); err != nil {
				return err
			}
			row := prepared.row
			userID, err := ensureUser(tx, input.Identity, now)
			if err != nil {
				return err
			}
			row.UserID = userID
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				createdAll = false
				if err := tx.Where("platform = ? AND dedupe_key = ?", input.Identity.Platform, input.DedupeKey).First(&row).Error; err != nil {
					return err
				}
				if row.JobID != input.JobID || row.ConversationType != input.Identity.ConversationType || row.ConversationID != input.Identity.ConversationID || row.ExternalUserID != input.Identity.UserID {
					return errors.New("capability execution dedupe row belongs to another conversation job")
				}
				if row.Capability != input.Capability || row.ArgumentsJSON != prepared.row.ArgumentsJSON || row.Effect != input.Effect {
					return errors.New("capability execution dedupe row does not match the requested invocation")
				}
			}
			rows[index] = row
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	executions := make([]CapabilityExecution, 0, len(rows))
	for _, row := range rows {
		execution, err := capabilityExecutionFromRow(row)
		if err != nil {
			return nil, false, err
		}
		executions = append(executions, execution)
	}
	return executions, createdAll, nil
}

type preparedCapabilityExecution struct {
	input CapabilityExecutionPrepare
	row   capabilityExecutionRow
}

func normalizeCapabilityExecutionPrepare(input CapabilityExecutionPrepare) (preparedCapabilityExecution, error) {
	if err := validateConversationIdentity(input.Identity); err != nil {
		return preparedCapabilityExecution{}, err
	}
	input.Identity = normalizeIdentity(input.Identity)
	if input.JobID <= 0 {
		return preparedCapabilityExecution{}, errors.New("capability execution job id is invalid")
	}
	if input.Sequence < 0 {
		return preparedCapabilityExecution{}, errors.New("capability execution sequence is invalid")
	}
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.DedupeKey = strings.TrimSpace(input.DedupeKey)
	input.Capability = strings.TrimSpace(input.Capability)
	input.Effect = strings.TrimSpace(input.Effect)
	if input.DedupeKey == "" || input.Capability == "" || input.Effect == "" {
		return preparedCapabilityExecution{}, errors.New("capability execution identity is incomplete")
	}
	argumentsJSON, err := json.Marshal(input.Arguments)
	if err != nil {
		return preparedCapabilityExecution{}, fmt.Errorf("encode capability arguments: %w", err)
	}
	receiptJSON, err := json.Marshal(input.Receipt)
	if err != nil {
		return preparedCapabilityExecution{}, fmt.Errorf("encode capability receipt: %w", err)
	}
	now := nowUTC()
	state := CapabilityExecutionApproved
	var startedAt *time.Time
	if input.RequiresConfirmation {
		state = CapabilityExecutionAwaitingConfirmation
	} else if strings.EqualFold(input.Effect, "read") {
		if input.LeaseToken == "" {
			return preparedCapabilityExecution{}, errors.New("running read capability requires a conversation job lease")
		}
		state = CapabilityExecutionRunning
		startedAt = &now
	}
	row := capabilityExecutionRow{
		ID:       capabilityExecutionID(input.Identity.Platform, input.DedupeKey),
		Platform: input.Identity.Platform, ExternalUserID: input.Identity.UserID,
		ConversationType: input.Identity.ConversationType, ConversationID: input.Identity.ConversationID,
		JobID: input.JobID, Sequence: input.Sequence, DedupeKey: input.DedupeKey,
		ToolCallID: strings.TrimSpace(input.ToolCallID), Capability: input.Capability,
		ArgumentsJSON: string(argumentsJSON), Effect: input.Effect, State: string(state),
		ReceiptJSON: string(receiptJSON), LeaseToken: capabilityExecutionLeaseToken(state, input.LeaseToken), StartedAt: startedAt, CreatedAt: now, UpdatedAt: now,
	}
	return preparedCapabilityExecution{input: input, row: row}, nil
}

func capabilityExecutionLeaseToken(state CapabilityExecutionState, leaseToken string) string {
	if state != CapabilityExecutionRunning {
		return ""
	}
	return strings.TrimSpace(leaseToken)
}

func (s *Store) CapabilityExecution(ctx context.Context, id string) (CapabilityExecution, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CapabilityExecution{}, false, errors.New("capability execution id is empty")
	}
	var row capabilityExecutionRow
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return CapabilityExecution{}, false, nil
	} else if err != nil {
		return CapabilityExecution{}, false, err
	}
	execution, err := capabilityExecutionFromRow(row)
	return execution, true, err
}

func (s *Store) CapabilityExecutionsForJob(ctx context.Context, jobID int64) ([]CapabilityExecution, error) {
	if jobID <= 0 {
		return nil, errors.New("capability execution job id is invalid")
	}
	var rows []capabilityExecutionRow
	if err := s.db.WithContext(ctx).Where("job_id = ?", jobID).Order("sequence ASC, created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]CapabilityExecution, 0, len(rows))
	for _, row := range rows {
		execution, err := capabilityExecutionFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, execution)
	}
	return result, nil
}

func (s *Store) UnsentCapabilityExecutionsForJob(ctx context.Context, jobID int64) ([]CapabilityExecution, error) {
	if jobID <= 0 {
		return nil, errors.New("capability execution job id is invalid")
	}
	var rows []capabilityExecutionRow
	if err := s.db.WithContext(ctx).Where("job_id = ? AND (receipt_state = '' OR receipt_state <> state)", jobID).
		Order("sequence ASC, created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]CapabilityExecution, 0, len(rows))
	for _, row := range rows {
		execution, err := capabilityExecutionFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, execution)
	}
	return result, nil
}

// ResolveCapabilityConfirmation consumes exactly the independently reversible
// operation whose confirmation receipt was durably committed for this actor's
// waiting job, then releases its owning job for a checkpoint resume. Other
// operations from the same grouped request remain awaiting confirmation until
// their own receipt is committed.
func (s *Store) ResolveCapabilityConfirmation(ctx context.Context, ident Identity, decision CapabilityConfirmationDecision, at ...time.Time) (*CapabilityExecution, *ConversationJob, error) {
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	if err := validateConversationIdentity(ident); err != nil {
		return nil, nil, err
	}
	ident = normalizeIdentity(ident)
	decision.SourceEventID = strings.TrimSpace(decision.SourceEventID)
	if decision.SourceEventID == "" {
		return nil, nil, errors.New("capability confirmation source event id is empty")
	}
	now := claimConversationJobTime(at)
	var resolved *CapabilityExecution
	var released *ConversationJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation capabilityExecutionRow
		// A confirmation event is consumed exactly once at the operation row.
		// Look it up before selecting a waiting operation so replay after the
		// job has produced its next prompt cannot approve that next operation.
		err := tx.Where("platform = ? AND confirmation_event_id = ?", ident.Platform, decision.SourceEventID).First(&operation).Error
		if err == nil {
			resolved, released, err = capabilityConfirmationResult(tx, operation)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		query := tx.Table("capability_executions AS operation").
			Select("operation.*").
			Joins("JOIN conversation_jobs AS job ON job.id = operation.job_id").
			Where("job.platform = ? AND job.conversation_type = ? AND job.conversation_id = ? AND job.external_user_id = ?",
				ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID).
			Where("job.state = ? AND job.expires_at > ?", string(ConversationJobStateWaitingConfirmation), now).
			Where("operation.state = ? AND operation.receipt_state = ?", string(CapabilityExecutionAwaitingConfirmation), string(CapabilityExecutionAwaitingConfirmation)).
			Joins("JOIN outgoing_messages AS confirmation ON confirmation.id = operation.confirmation_outbox_id AND confirmation.status = ?", string(delivery.StatusAccepted)).
			Order("job.sequence ASC, operation.sequence ASC, operation.created_at ASC, operation.id ASC")
		err = query.First(&operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		state := CapabilityExecutionDenied
		reason := strings.TrimSpace(decision.Reason)
		if decision.Approved {
			state = CapabilityExecutionApproved
			reason = ""
		} else if reason == "" {
			reason = "用户拒绝执行"
		}
		result := tx.Model(&capabilityExecutionRow{}).
			Where("id = ? AND state = ?", operation.ID, string(CapabilityExecutionAwaitingConfirmation)).
			Updates(map[string]any{
				"state": string(state), "error": reason, "confirmation_event_id": decision.SourceEventID,
				"confirmed_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		result = tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND expires_at > ?", operation.JobID, string(ConversationJobStateWaitingConfirmation), now).
			Updates(map[string]any{
				"state": string(ConversationJobStateQueued), "wait_reason": "", "retry_at": nil,
				"revision": gorm.Expr("revision + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("capability confirmation lost its waiting job")
		}
		if err := tx.Where("id = ?", operation.ID).First(&operation).Error; err != nil {
			return err
		}
		resolved, released, err = capabilityConfirmationResult(tx, operation)
		return err
	})
	return resolved, released, err
}

func capabilityConfirmationResult(tx *gorm.DB, operation capabilityExecutionRow) (*CapabilityExecution, *ConversationJob, error) {
	var jobRow conversationJobRow
	if err := tx.Where("id = ?", operation.JobID).First(&jobRow).Error; err != nil {
		return nil, nil, err
	}
	execution, err := capabilityExecutionFromRow(operation)
	if err != nil {
		return nil, nil, err
	}
	job, err := conversationJobFromRow(jobRow)
	if err != nil {
		return nil, nil, err
	}
	return &execution, &job, nil
}

// ClaimCapabilityExecutionForJob is the mutation commit gate. Only a
// host-approved row, or an operation explicitly paused before execution for
// authentication, may move to running while its owning conversation job is
// still running under the caller's current lease token. Cancellation, expiry,
// lease recovery, and a competing worker therefore all win atomically over a
// later claim.
func (s *Store) ClaimCapabilityExecutionForJob(ctx context.Context, id string, jobID int64, leaseToken string) (CapabilityExecution, bool, error) {
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if id == "" {
		return CapabilityExecution{}, false, errors.New("capability execution id is empty")
	}
	if jobID <= 0 {
		return CapabilityExecution{}, false, errors.New("capability execution job id is invalid")
	}
	if leaseToken == "" {
		return CapabilityExecution{}, false, errors.New("conversation job lease token is empty")
	}
	now := nowUTC()
	var execution CapabilityExecution
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row capabilityExecutionRow
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		result := tx.Model(&capabilityExecutionRow{}).
			Where("id = ? AND job_id = ? AND (state IN ? OR (state = ? AND LOWER(effect) = LOWER(?) AND COALESCE(lease_token, '') <> ?))",
				id, jobID, []string{string(CapabilityExecutionApproved), string(CapabilityExecutionWaitingAuth)},
				string(CapabilityExecutionRunning), "read", leaseToken).
			Where(`EXISTS (
				SELECT 1 FROM conversation_jobs job
				WHERE job.id = capability_executions.job_id
				  AND job.state = ?
				  AND job.lease_token = ?
				  AND (job.expires_at IS NULL OR job.expires_at > ?)
			)`, string(ConversationJobStateRunning), leaseToken, now).
			Updates(map[string]any{"state": string(CapabilityExecutionRunning), "lease_token": leaseToken, "started_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		var err error
		execution, err = capabilityExecutionFromRow(row)
		return err
	})
	return execution, claimed, err
}

// DeferCapabilityExecutionForAuth records that no external effect completed
// because authentication is required. The owning job can safely wait for the
// auth poller and claim this exact operation again after authorization.
func (s *Store) DeferCapabilityExecutionForAuth(ctx context.Context, id, leaseToken string) (CapabilityExecution, error) {
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if id == "" {
		return CapabilityExecution{}, errors.New("capability execution id is empty")
	}
	if leaseToken == "" {
		return CapabilityExecution{}, errors.New("capability execution lease token is empty")
	}
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ? AND lease_token = ?", id, string(CapabilityExecutionRunning), leaseToken).
		Where(currentCapabilityJobLeasePredicate, string(ConversationJobStateRunning), leaseToken, now).
		Updates(map[string]any{
			"state": string(CapabilityExecutionWaitingAuth), "started_at": nil,
			"lease_token": "", "result": "", "error": "", "updated_at": now,
		})
	if result.Error != nil {
		return CapabilityExecution{}, result.Error
	}
	if result.RowsAffected != 1 {
		return CapabilityExecution{}, errors.New("capability execution is not running")
	}
	execution, _, err := s.CapabilityExecution(ctx, id)
	return execution, err
}

func (s *Store) FinishCapabilityExecution(ctx context.Context, id, leaseToken, resultText string, runErr error) (CapabilityExecution, error) {
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if id == "" {
		return CapabilityExecution{}, errors.New("capability execution id is empty")
	}
	if leaseToken == "" {
		return CapabilityExecution{}, errors.New("capability execution lease token is empty")
	}
	now := nowUTC()
	state := CapabilityExecutionSucceeded
	errorText := ""
	if runErr != nil {
		state = CapabilityExecutionFailed
		errorText = strings.TrimSpace(runErr.Error())
	}
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ? AND lease_token = ?", id, string(CapabilityExecutionRunning), leaseToken).
		Where(currentCapabilityJobLeasePredicate, string(ConversationJobStateRunning), leaseToken, now).
		Updates(map[string]any{
			"state": string(state), "result": resultText, "error": errorText,
			"finished_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return CapabilityExecution{}, result.Error
	}
	if result.RowsAffected != 1 {
		return CapabilityExecution{}, errors.New("capability execution is not running")
	}
	execution, _, err := s.CapabilityExecution(ctx, id)
	return execution, err
}

// FinishCapabilityExecutionUnknown records a typed ambiguous domain outcome.
// The descriptor-owned result is retained for replay; reason is diagnostic
// metadata and is never the model-facing evidence.
func (s *Store) FinishCapabilityExecutionUnknown(ctx context.Context, id, leaseToken, resultText, reason string) (CapabilityExecution, error) {
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if id == "" {
		return CapabilityExecution{}, errors.New("capability execution id is empty")
	}
	if leaseToken == "" {
		return CapabilityExecution{}, errors.New("capability execution lease token is empty")
	}
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ? AND lease_token = ?", id, string(CapabilityExecutionRunning), leaseToken).
		Where(currentCapabilityJobLeasePredicate, string(ConversationJobStateRunning), leaseToken, now).
		Updates(map[string]any{
			"state": string(CapabilityExecutionUnknown), "result": resultText, "error": strings.TrimSpace(reason),
			"finished_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return CapabilityExecution{}, result.Error
	}
	if result.RowsAffected != 1 {
		return CapabilityExecution{}, errors.New("capability execution is not running")
	}
	execution, _, err := s.CapabilityExecution(ctx, id)
	return execution, err
}

// MarkStaleCapabilityExecutionUnknown lets the current job owner resolve a
// mutation left running by an older lease. The job-lease predicate prevents
// that older worker from marking a newer worker's active operation unknown.
func (s *Store) MarkStaleCapabilityExecutionUnknown(ctx context.Context, id string, jobID int64, currentLeaseToken, reason string) (CapabilityExecution, bool, error) {
	id = strings.TrimSpace(id)
	currentLeaseToken = strings.TrimSpace(currentLeaseToken)
	if id == "" {
		return CapabilityExecution{}, false, errors.New("capability execution id is empty")
	}
	if jobID <= 0 {
		return CapabilityExecution{}, false, errors.New("capability execution job id is invalid")
	}
	if currentLeaseToken == "" {
		return CapabilityExecution{}, false, errors.New("conversation job lease token is empty")
	}
	now := nowUTC()
	var execution CapabilityExecution
	marked := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&capabilityExecutionRow{}).
			Where("id = ? AND job_id = ? AND state = ? AND LOWER(effect) <> LOWER(?) AND COALESCE(lease_token, '') <> ?",
				id, jobID, string(CapabilityExecutionRunning), "read", currentLeaseToken).
			Where(`EXISTS (
				SELECT 1 FROM conversation_jobs job
				WHERE job.id = capability_executions.job_id
				  AND job.state = ?
				  AND job.lease_token = ?
				  AND (job.expires_at IS NULL OR job.expires_at > ?)
			)`, string(ConversationJobStateRunning), currentLeaseToken, now).
			Updates(map[string]any{
				"state": string(CapabilityExecutionUnknown), "error": strings.TrimSpace(reason),
				"finished_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		marked = result.RowsAffected == 1
		var row capabilityExecutionRow
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		var err error
		execution, err = capabilityExecutionFromRow(row)
		return err
	})
	return execution, marked, err
}

func (s *Store) UpdateCapabilityExecutionReceipt(ctx context.Context, id, leaseToken string, receipt CapabilityReceipt) error {
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if id == "" {
		return errors.New("capability execution id is empty")
	}
	if leaseToken == "" {
		return errors.New("capability execution lease token is empty")
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode capability receipt: %w", err)
	}
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ? AND lease_token = ?", id, string(CapabilityExecutionRunning), leaseToken).
		Where(currentCapabilityJobLeasePredicate, string(ConversationJobStateRunning), leaseToken, now).
		Updates(map[string]any{"receipt_json": string(receiptJSON), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("capability execution lease is not current")
	}
	return nil
}

const currentCapabilityJobLeasePredicate = `EXISTS (
	SELECT 1 FROM conversation_jobs job
	WHERE job.id = capability_executions.job_id
	  AND job.state = ?
	  AND job.lease_token = ?
	  AND (job.expires_at IS NULL OR job.expires_at > ?)
)`

func capabilityExecutionFromRow(row capabilityExecutionRow) (CapabilityExecution, error) {
	var arguments []string
	if err := json.Unmarshal([]byte(row.ArgumentsJSON), &arguments); err != nil {
		return CapabilityExecution{}, fmt.Errorf("decode capability execution %s arguments: %w", row.ID, err)
	}
	var receipt CapabilityReceipt
	if err := json.Unmarshal([]byte(row.ReceiptJSON), &receipt); err != nil {
		return CapabilityExecution{}, fmt.Errorf("decode capability execution %s receipt: %w", row.ID, err)
	}
	confirmationEventID := ""
	if row.ConfirmationEventID != nil {
		confirmationEventID = *row.ConfirmationEventID
	}
	return CapabilityExecution{
		ID:       row.ID,
		Identity: Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		JobID:    row.JobID, Sequence: row.Sequence, DedupeKey: row.DedupeKey, ToolCallID: row.ToolCallID, LeaseToken: row.LeaseToken,
		Capability: row.Capability, Arguments: arguments, Effect: row.Effect,
		State: CapabilityExecutionState(row.State), Receipt: receipt, Result: row.Result, Error: row.Error,
		ConfirmationOutboxID: row.ConfirmationOutboxID, ConfirmationEventID: confirmationEventID,
		ConfirmedAt: row.ConfirmedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		ReceiptSentAt: row.ReceiptSentAt,
		ReceiptState:  CapabilityExecutionState(row.ReceiptState),
		CreatedAt:     row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func capabilityExecutionID(platform, dedupeKey string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(platform) + "\x00" + strings.TrimSpace(dedupeKey)))
	return "op_" + hex.EncodeToString(digest[:16])
}
