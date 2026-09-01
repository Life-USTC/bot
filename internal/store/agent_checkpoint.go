package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AgentCheckpointStore persists Eino's opaque checkpoint payloads. The
// payload is deliberately kept out of interactions and logs: it may contain
// private tool arguments and model context.
type AgentCheckpointStore struct {
	store *Store
}

type agentCheckpointRow struct {
	ID        string `gorm:"primaryKey"`
	Payload   []byte `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (agentCheckpointRow) TableName() string { return "agent_checkpoints" }

func (s *Store) AgentCheckpoints() *AgentCheckpointStore {
	if s == nil {
		return nil
	}
	return &AgentCheckpointStore{store: s}
}

func (s *AgentCheckpointStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("agent checkpoint store is unavailable")
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, false, errors.New("agent checkpoint id is empty")
	}
	var row agentCheckpointRow
	err := s.store.db.WithContext(ctx).Where("id = ?", checkpointID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), row.Payload...), true, nil
}

func (s *AgentCheckpointStore) Set(ctx context.Context, checkpointID string, payload []byte) error {
	if s == nil || s.store == nil {
		return errors.New("agent checkpoint store is unavailable")
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return errors.New("agent checkpoint id is empty")
	}
	if len(payload) == 0 {
		return errors.New("agent checkpoint payload is empty")
	}
	now := nowUTC()
	row := agentCheckpointRow{
		ID: checkpointID, Payload: append([]byte(nil), payload...), CreatedAt: now, UpdatedAt: now,
	}
	return s.store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"payload": row.Payload, "updated_at": now,
		}),
	}).Create(&row).Error
}

func (s *AgentCheckpointStore) Delete(ctx context.Context, checkpointID string) error {
	if s == nil || s.store == nil {
		return errors.New("agent checkpoint store is unavailable")
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return errors.New("agent checkpoint id is empty")
	}
	return s.store.db.WithContext(ctx).Where("id = ?", checkpointID).Delete(&agentCheckpointRow{}).Error
}
