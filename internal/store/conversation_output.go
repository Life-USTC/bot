package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"gorm.io/gorm"
)

// ConversationJobOutputCommit is the single durable boundary between a
// running conversation job and user-visible delivery. Outbox messages,
// receipt state, and the job transition either all commit or all roll back.
type ConversationJobOutputCommit struct {
	JobID      int64
	LeaseToken string
	Messages   []message.Outbound
	ReceiptIDs []string
	Transition ConversationJobTransition
}

type ConversationJobCommittedOutput struct {
	Record  delivery.Record
	Created bool
}

// ConversationJobProgressEnqueue is the lease-checked input for the single
// best-effort progress message belonging to a running conversation job. The
// store owns the idempotency key so progress can never share the key space of
// a final response.
type ConversationJobProgressEnqueue struct {
	JobID      int64
	LeaseToken string
	Message    message.Outbound
}

// RetryConversationJob releases a running lease into durable retry_wait. It
// deliberately changes no operation rows: a later attempt resumes from the
// authoritative capability state and reuses the same output dedupe keys.
func (s *Store) RetryConversationJob(ctx context.Context, id int64, leaseToken, reason string) (bool, error) {
	return s.TransitionConversationJob(ctx, id, leaseToken, ConversationJobTransition{
		State:     ConversationJobStateRetryWait,
		RetryAt:   nowUTC(),
		LastError: reason,
	})
}

// EnqueueConversationJobProgress atomically checks that the supplied lease is
// still current before inserting the one progress outbox row. A false result
// means the worker lost its lease or the job already resolved; in either case
// no message is inserted.
func (s *Store) EnqueueConversationJobProgress(ctx context.Context, input ConversationJobProgressEnqueue) (delivery.Record, bool, error) {
	if input.JobID <= 0 {
		return delivery.Record{}, false, errors.New("conversation job id is invalid")
	}
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if input.LeaseToken == "" {
		return delivery.Record{}, false, errors.New("conversation job lease token is empty")
	}
	input.Message.DedupeKey = fmt.Sprintf("conversation-job:%d:progress", input.JobID)
	now := nowUTC()

	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()

	var record delivery.Record
	var created bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The matched update acquires SQLite's writer lock before the outbox
		// insert. This serializes progress with the final-output transaction even
		// when two Store instances share the same database.
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND lease_token = ? AND expires_at > ?",
				input.JobID, string(ConversationJobStateRunning), input.LeaseToken, now).
			Updates(map[string]any{"updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		var err error
		record, created, err = enqueueWithDB(tx, input.Message, now)
		return err
	})
	if err != nil {
		return delivery.Record{}, false, err
	}
	return record, created, nil
}

func (s *Store) CommitConversationJobOutput(ctx context.Context, commit ConversationJobOutputCommit) ([]ConversationJobCommittedOutput, error) {
	if commit.JobID <= 0 {
		return nil, errors.New("conversation job id is invalid")
	}
	commit.LeaseToken = strings.TrimSpace(commit.LeaseToken)
	if commit.LeaseToken == "" {
		return nil, errors.New("conversation job lease token is empty")
	}
	if err := validateConversationJobTransition(commit.Transition); err != nil {
		return nil, err
	}
	updates, err := conversationJobTransitionUpdates(commit.Transition)
	if err != nil {
		return nil, err
	}
	if conversationJobStateIsTerminal(commit.Transition.State) {
		updates["terminal_lease_token"] = commit.LeaseToken
	} else {
		updates["terminal_lease_token"] = ""
	}
	now := nowUTC()
	updates["updated_at"] = now
	receiptIDs := uniqueNonEmptyStrings(commit.ReceiptIDs)
	outputs := make([]ConversationJobCommittedOutput, 0, len(commit.Messages))

	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND lease_token = ? AND (expires_at IS NULL OR expires_at > ?)",
				commit.JobID, string(ConversationJobStateRunning), commit.LeaseToken, now).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("conversation job output commit lost its lease")
		}
		if err := finalizeCapabilityExecutionsForJobTransition(tx, commit.JobID, commit.LeaseToken, commit.Transition, now); err != nil {
			return err
		}
		if err := tx.Model(&outgoingMessageRow{}).
			Where("dedupe_key = ? AND status IN ?", fmt.Sprintf("conversation-job:%d:progress", commit.JobID), []string{
				string(delivery.StatusPending), string(delivery.StatusRetryWait),
			}).
			Updates(map[string]any{
				"status":             string(delivery.StatusExpired),
				"next_attempt_at":    nil,
				"attempt_started_at": nil,
				"error_code":         "superseded",
				"error_message":      "superseded by conversation job output",
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}
		for _, outbound := range commit.Messages {
			record, created, err := enqueueWithDB(tx, outbound, now)
			if err != nil {
				return err
			}
			outputs = append(outputs, ConversationJobCommittedOutput{Record: record, Created: created})
		}
		if len(receiptIDs) > 0 {
			if err := tx.Model(&capabilityExecutionRow{}).
				Where("job_id = ? AND id IN ? AND (receipt_state = '' OR receipt_state <> state)", commit.JobID, receiptIDs).
				Updates(map[string]any{
					"receipt_sent_at": now, "receipt_state": gorm.Expr("state"), "updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return outputs, err
}

func uniqueNonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
