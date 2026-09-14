package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// RecordAgentModelAttempt durably records one physical provider attempt before
// the network call starts. It has no run-wide cap: the count is retained for
// billing and crash recovery even when the provider fails or the process exits
// before a response can be decoded.
func (s *Store) RecordAgentModelAttempt(ctx context.Context, runID int64) error {
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	if runID <= 0 {
		return errors.New("agent model attempt is invalid")
	}
	result := s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("id = ? AND status = ?", runID, AgentRunStatusStarted).
		UpdateColumn("model_requests", gorm.Expr("model_requests + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("agent run is no longer active")
	}
	return nil
}

// RecordAgentUsage durably records provider-reported usage as soon as a
// successful response is decoded. Attempt counts are recorded separately before
// each provider call, so a billable failed attempt remains visible as well.
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
