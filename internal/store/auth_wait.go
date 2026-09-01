package store

import (
	"context"
	"time"
)

// WaitingAuthConversationJobs returns unexpired auth waits for host-side
// authorization validation. The auth package owns the OAuth scope contract;
// the store deliberately does not infer authorization from credential
// existence alone.
func (s *Store) WaitingAuthConversationJobs(ctx context.Context, now time.Time) ([]ConversationJob, error) {
	now = normalizeStoreTime(now)
	var rows []conversationJobRow
	if err := s.db.WithContext(ctx).
		Where("state = ? AND (expires_at IS NULL OR expires_at > ?)", string(ConversationJobStateWaitingAuth), now).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]ConversationJob, 0, len(rows))
	for _, row := range rows {
		job, err := conversationJobFromRow(row)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}
