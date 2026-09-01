package store

import (
	"context"
	"errors"
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
	now := nowUTC()
	updates["updated_at"] = now
	receiptIDs := uniqueNonEmptyStrings(commit.ReceiptIDs)
	outputs := make([]ConversationJobCommittedOutput, 0, len(commit.Messages))

	s.conversationJobMu.Lock()
	defer s.conversationJobMu.Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND lease_token = ?", commit.JobID, string(ConversationJobStateRunning), commit.LeaseToken).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("conversation job output commit lost its lease")
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
