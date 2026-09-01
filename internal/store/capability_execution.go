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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CapabilityExecutionState string

const (
	CapabilityExecutionAwaitingConfirmation CapabilityExecutionState = "awaiting_confirmation"
	CapabilityExecutionApproved             CapabilityExecutionState = "approved"
	CapabilityExecutionDenied               CapabilityExecutionState = "denied"
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
	ID          string
	Identity    Identity
	JobID       int64
	Sequence    int
	DedupeKey   string
	ToolCallID  string
	Capability  string
	Arguments   []string
	Effect      string
	State       CapabilityExecutionState
	Receipt     CapabilityReceipt
	Result      string
	Error       string
	ConfirmedAt *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CapabilityExecutionPrepare struct {
	Identity             Identity
	JobID                int64
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
	Approved bool
	Reason   string
}

type capabilityExecutionRow struct {
	ID               string `gorm:"primaryKey"`
	UserID           int64  `gorm:"not null;index"`
	Platform         string `gorm:"not null;uniqueIndex:idx_capability_executions_dedupe,priority:1;index:idx_capability_executions_identity_state,priority:1"`
	ExternalUserID   string `gorm:"not null;index:idx_capability_executions_identity_state,priority:4"`
	ConversationType string `gorm:"not null;index:idx_capability_executions_identity_state,priority:2"`
	ConversationID   string `gorm:"not null;index:idx_capability_executions_identity_state,priority:3"`
	JobID            int64  `gorm:"not null;index:idx_capability_executions_job_sequence,priority:1"`
	Sequence         int    `gorm:"not null;index:idx_capability_executions_job_sequence,priority:2"`
	DedupeKey        string `gorm:"not null;uniqueIndex:idx_capability_executions_dedupe,priority:2"`
	ToolCallID       string `gorm:"not null;default:'';index"`
	Capability       string `gorm:"not null"`
	ArgumentsJSON    string `gorm:"not null"`
	Effect           string `gorm:"not null"`
	State            string `gorm:"not null;index:idx_capability_executions_identity_state,priority:5"`
	ReceiptJSON      string `gorm:"not null;default:'{}'"`
	Result           string
	Error            string
	ConfirmedAt      *time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (capabilityExecutionRow) TableName() string { return "capability_executions" }

func (s *Store) PrepareCapabilityExecution(ctx context.Context, input CapabilityExecutionPrepare) (CapabilityExecution, bool, error) {
	if err := validateConversationIdentity(input.Identity); err != nil {
		return CapabilityExecution{}, false, err
	}
	input.Identity = normalizeIdentity(input.Identity)
	if input.JobID <= 0 {
		return CapabilityExecution{}, false, errors.New("capability execution job id is invalid")
	}
	if input.Sequence < 0 {
		return CapabilityExecution{}, false, errors.New("capability execution sequence is invalid")
	}
	input.DedupeKey = strings.TrimSpace(input.DedupeKey)
	input.Capability = strings.TrimSpace(input.Capability)
	input.Effect = strings.TrimSpace(input.Effect)
	if input.DedupeKey == "" || input.Capability == "" || input.Effect == "" {
		return CapabilityExecution{}, false, errors.New("capability execution identity is incomplete")
	}
	argumentsJSON, err := json.Marshal(input.Arguments)
	if err != nil {
		return CapabilityExecution{}, false, fmt.Errorf("encode capability arguments: %w", err)
	}
	receiptJSON, err := json.Marshal(input.Receipt)
	if err != nil {
		return CapabilityExecution{}, false, fmt.Errorf("encode capability receipt: %w", err)
	}
	now := nowUTC()
	state := CapabilityExecutionRunning
	var startedAt *time.Time
	if input.RequiresConfirmation {
		state = CapabilityExecutionAwaitingConfirmation
	} else {
		startedAt = &now
	}
	row := capabilityExecutionRow{
		ID:       capabilityExecutionID(input.Identity.Platform, input.DedupeKey),
		Platform: input.Identity.Platform, ExternalUserID: input.Identity.UserID,
		ConversationType: input.Identity.ConversationType, ConversationID: input.Identity.ConversationID,
		JobID: input.JobID, Sequence: input.Sequence, DedupeKey: input.DedupeKey,
		ToolCallID: strings.TrimSpace(input.ToolCallID), Capability: input.Capability,
		ArgumentsJSON: string(argumentsJSON), Effect: input.Effect, State: string(state),
		ReceiptJSON: string(receiptJSON), StartedAt: startedAt, CreatedAt: now, UpdatedAt: now,
	}
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		userID, err := ensureUser(tx, input.Identity, now)
		if err != nil {
			return err
		}
		row.UserID = userID
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if !created {
			return tx.Where("platform = ? AND dedupe_key = ?", input.Identity.Platform, input.DedupeKey).First(&row).Error
		}
		return nil
	})
	if err != nil {
		return CapabilityExecution{}, false, err
	}
	execution, err := capabilityExecutionFromRow(row)
	return execution, created, err
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

// ResolveCapabilityConfirmation consumes exactly one independently reversible
// operation, then releases its owning job for a checkpoint resume. Other
// operations from the same grouped request remain awaiting confirmation.
func (s *Store) ResolveCapabilityConfirmation(ctx context.Context, ident Identity, decision CapabilityConfirmationDecision, at ...time.Time) (*CapabilityExecution, *ConversationJob, error) {
	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	if err := validateConversationIdentity(ident); err != nil {
		return nil, nil, err
	}
	ident = normalizeIdentity(ident)
	now := claimConversationJobTime(at)
	var resolved *CapabilityExecution
	var released *ConversationJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation capabilityExecutionRow
		err := tx.Table("capability_executions AS operation").
			Select("operation.*").
			Joins("JOIN conversation_jobs AS job ON job.id = operation.job_id").
			Where("job.platform = ? AND job.conversation_type = ? AND job.conversation_id = ? AND job.external_user_id = ?",
				ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID).
			Where("job.state = ? AND job.expires_at > ?", string(ConversationJobStateWaitingConfirmation), now).
			Where("operation.state = ?", string(CapabilityExecutionAwaitingConfirmation)).
			Order("job.sequence ASC, operation.sequence ASC, operation.created_at ASC, operation.id ASC").
			First(&operation).Error
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
				"state": string(state), "error": reason, "confirmed_at": now, "updated_at": now,
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
		var jobRow conversationJobRow
		if err := tx.Where("id = ?", operation.JobID).First(&jobRow).Error; err != nil {
			return err
		}
		execution, err := capabilityExecutionFromRow(operation)
		if err != nil {
			return err
		}
		job, err := conversationJobFromRow(jobRow)
		if err != nil {
			return err
		}
		resolved = &execution
		released = &job
		return nil
	})
	return resolved, released, err
}

// ClaimCapabilityExecution is the mutation commit gate. Only a host-approved
// row may move to running; a replay sees the persisted terminal/running state
// and must not invoke the external mutation again.
func (s *Store) ClaimCapabilityExecution(ctx context.Context, id string) (CapabilityExecution, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CapabilityExecution{}, false, errors.New("capability execution id is empty")
	}
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ?", id, string(CapabilityExecutionApproved)).
		Updates(map[string]any{"state": string(CapabilityExecutionRunning), "started_at": now, "updated_at": now})
	if result.Error != nil {
		return CapabilityExecution{}, false, result.Error
	}
	execution, found, err := s.CapabilityExecution(ctx, id)
	return execution, found && result.RowsAffected == 1, err
}

func (s *Store) FinishCapabilityExecution(ctx context.Context, id string, resultText string, runErr error) (CapabilityExecution, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CapabilityExecution{}, errors.New("capability execution id is empty")
	}
	now := nowUTC()
	state := CapabilityExecutionSucceeded
	errorText := ""
	if runErr != nil {
		state = CapabilityExecutionFailed
		errorText = strings.TrimSpace(runErr.Error())
	}
	result := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ?", id, string(CapabilityExecutionRunning)).
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

func (s *Store) MarkCapabilityExecutionUnknown(ctx context.Context, id, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("capability execution id is empty")
	}
	now := nowUTC()
	return s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).
		Where("id = ? AND state = ?", id, string(CapabilityExecutionRunning)).
		Updates(map[string]any{
			"state": string(CapabilityExecutionUnknown), "error": strings.TrimSpace(reason),
			"finished_at": now, "updated_at": now,
		}).Error
}

func (s *Store) UpdateCapabilityExecutionReceipt(ctx context.Context, id string, receipt CapabilityReceipt) error {
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode capability receipt: %w", err)
	}
	return s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{"receipt_json": string(receiptJSON), "updated_at": nowUTC()}).Error
}

func capabilityExecutionFromRow(row capabilityExecutionRow) (CapabilityExecution, error) {
	var arguments []string
	if err := json.Unmarshal([]byte(row.ArgumentsJSON), &arguments); err != nil {
		return CapabilityExecution{}, fmt.Errorf("decode capability execution %s arguments: %w", row.ID, err)
	}
	var receipt CapabilityReceipt
	if err := json.Unmarshal([]byte(row.ReceiptJSON), &receipt); err != nil {
		return CapabilityExecution{}, fmt.Errorf("decode capability execution %s receipt: %w", row.ID, err)
	}
	return CapabilityExecution{
		ID:       row.ID,
		Identity: Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		JobID:    row.JobID, Sequence: row.Sequence, DedupeKey: row.DedupeKey, ToolCallID: row.ToolCallID,
		Capability: row.Capability, Arguments: arguments, Effect: row.Effect,
		State: CapabilityExecutionState(row.State), Receipt: receipt, Result: row.Result, Error: row.Error,
		ConfirmedAt: row.ConfirmedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func capabilityExecutionID(platform, dedupeKey string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(platform) + "\x00" + strings.TrimSpace(dedupeKey)))
	return "op_" + hex.EncodeToString(digest[:16])
}
