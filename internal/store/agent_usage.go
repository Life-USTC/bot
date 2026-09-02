package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Mirrors the Agent's maximum of 13 logical requests (12 tool calls plus the
// final answer), each with up to five physical provider attempts. Keeping a
// hard store-side ceiling prevents callers from bypassing the durable budget.
const agentModelAttemptLimit int64 = 65

// ReserveAgentModelAttempt atomically reserves one physical provider attempt
// for a started run. For a conversation job the aggregate predicate covers
// every run (including interrupted runs), so a resumed worker cannot recover a
// fresh attempt budget after a process crash.
//
// The bool is false when the run is no longer reservable or the durable limit
// has already been exhausted.
func (s *Store) ReserveAgentModelAttempt(ctx context.Context, runID, jobID, limit int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("store is unavailable")
	}
	if runID <= 0 || limit <= 0 {
		return false, errors.New("agent model attempt reservation is invalid")
	}
	if limit > agentModelAttemptLimit {
		limit = agentModelAttemptLimit
	}
	query := s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("id = ? AND status = ?", runID, AgentRunStatusStarted)
	if jobID > 0 {
		query = query.Where("job_id = ?", jobID).
			Where("(SELECT COALESCE(SUM(model_requests), 0) FROM agent_runs WHERE job_id = ?) < ?", jobID, limit)
	} else {
		// Runs without a conversation job still use the process-local budget;
		// keep their persisted usage monotonic if a provider response is recorded.
		query = query.Where("model_requests < ?", limit)
	}
	result := query.UpdateColumn("model_requests", gorm.Expr("model_requests + 1"))
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RecordAgentUsage durably records provider-reported usage as soon as a
// successful response is decoded. The reservation count is intentionally not
// reduced or replaced; an attempt can be billable even when its response is
// unavailable to the process that started it.
func (s *Store) RecordAgentUsage(ctx context.Context, runID int64, spending AgentSpending) error {
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	if runID <= 0 {
		return errors.New("agent run id is invalid")
	}
	updates := map[string]any{
		"prompt_tokens":     gorm.Expr("prompt_tokens + ?", spending.PromptTokens),
		"cached_tokens":     gorm.Expr("cached_tokens + ?", spending.CachedTokens),
		"completion_tokens": gorm.Expr("completion_tokens + ?", spending.CompletionTokens),
		"total_tokens":      gorm.Expr("total_tokens + ?", spending.TotalTokens),
		"cost_nano_cny":     gorm.Expr("cost_nano_cny + ?", spending.CostNanoCNY),
		"updated_at":        nowUTC(),
	}
	result := s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("id = ?", runID).Updates(updates)
	return result.Error
}
