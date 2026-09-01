package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrAgentCheckpointClaimRequired prevents an opaque checkpoint store from
	// being used without the conversation-job claim that owns its writes.
	ErrAgentCheckpointClaimRequired = errors.New("agent checkpoint job claim is required")
	// ErrAgentCheckpointClaimMismatch means the caller no longer owns the
	// checkpoint revision/lease it attempted to read, write, or delete.
	ErrAgentCheckpointClaimMismatch = errors.New("agent checkpoint job claim does not match")
)

// AgentCheckpointClaim binds an opaque Eino checkpoint to one claimed
// conversation-job revision. The lease token distinguishes recovery claims
// that reuse the same revision.
type AgentCheckpointClaim struct {
	JobID      int64
	Revision   int
	LeaseToken string
}

// AgentCheckpointStore persists Eino's opaque checkpoint payloads. The
// payload is deliberately kept out of interactions and logs: it may contain
// private tool arguments and model context.
type AgentCheckpointStore struct {
	store *Store
}

// BoundAgentCheckpointStore is the only checkpoint store that permits writes.
// Every operation carries the claim captured by the worker that owns the
// conversation job.
type BoundAgentCheckpointStore struct {
	store *AgentCheckpointStore
	claim AgentCheckpointClaim
}

type agentCheckpointRow struct {
	ID         string `gorm:"primaryKey"`
	JobID      int64  `gorm:"not null;default:0;index"`
	Revision   int    `gorm:"not null;default:0;index"`
	LeaseToken string `gorm:"not null;default:'';index"`
	Payload    []byte `gorm:"not null"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (agentCheckpointRow) TableName() string { return "agent_checkpoints" }

func (s *Store) AgentCheckpoints() *AgentCheckpointStore {
	if s == nil {
		return nil
	}
	return &AgentCheckpointStore{store: s}
}

// Bind returns a claim-bound checkpoint store for one running job. The
// unbound AgentCheckpointStore intentionally cannot Set or Delete.
func (s *AgentCheckpointStore) Bind(claim AgentCheckpointClaim) (*BoundAgentCheckpointStore, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("agent checkpoint store is unavailable")
	}
	claim.LeaseToken = strings.TrimSpace(claim.LeaseToken)
	if claim.JobID <= 0 || claim.Revision <= 0 || claim.LeaseToken == "" {
		return nil, ErrAgentCheckpointClaimRequired
	}
	return &BoundAgentCheckpointStore{store: s, claim: claim}, nil
}

// Get remains available for maintenance and diagnostics. Agent runners must
// use a bound store so a stale worker cannot resume a newer checkpoint.
func (s *AgentCheckpointStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("agent checkpoint store is unavailable")
	}
	checkpointID, err := normalizeAgentCheckpointID(checkpointID)
	if err != nil {
		return nil, false, err
	}
	var row agentCheckpointRow
	err = s.store.db.WithContext(ctx).Where("id = ?", checkpointID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), row.Payload...), true, nil
}

func (s *BoundAgentCheckpointStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("agent checkpoint store is unavailable")
	}
	checkpointID, err := normalizeAgentCheckpointID(checkpointID)
	if err != nil {
		return nil, false, err
	}
	var payload []byte
	err = s.store.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the current claim before adopting an older checkpoint binding.
		// A resumed worker is allowed to take ownership of the checkpoint left
		// by its predecessor, while a stale worker cannot do so after losing its
		// lease.
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND revision = ? AND lease_token = ?",
				s.claim.JobID, string(ConversationJobStateRunning), s.claim.Revision, s.claim.LeaseToken).
			UpdateColumn("updated_at", gorm.Expr("updated_at"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgentCheckpointClaimMismatch
		}
		var row agentCheckpointRow
		err := tx.Where("id = ?", checkpointID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.JobID != s.claim.JobID {
			return ErrAgentCheckpointClaimMismatch
		}
		if !s.rowMatchesClaim(row) {
			if err := tx.Model(&agentCheckpointRow{}).Where("id = ?", checkpointID).Updates(map[string]any{
				"job_id": s.claim.JobID, "revision": s.claim.Revision, "lease_token": s.claim.LeaseToken,
				"updated_at": nowUTC(),
			}).Error; err != nil {
				return err
			}
		}
		payload = append([]byte(nil), row.Payload...)
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if payload == nil {
		return nil, false, nil
	}
	return payload, true, nil
}

func (s *BoundAgentCheckpointStore) Set(ctx context.Context, checkpointID string, payload []byte) error {
	if s == nil || s.store == nil {
		return errors.New("agent checkpoint store is unavailable")
	}
	checkpointID, err := normalizeAgentCheckpointID(checkpointID)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("agent checkpoint payload is empty")
	}
	now := nowUTC()
	return s.store.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A no-op UPDATE takes SQLite's writer lock before the claim check and
		// upsert. A transition or acknowledgement therefore cannot race between
		// validation and checkpoint creation/replacement.
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND state = ? AND revision = ? AND lease_token = ?",
				s.claim.JobID, string(ConversationJobStateRunning), s.claim.Revision, s.claim.LeaseToken).
			UpdateColumn("updated_at", gorm.Expr("updated_at"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgentCheckpointClaimMismatch
		}
		row := agentCheckpointRow{
			ID: checkpointID, JobID: s.claim.JobID, Revision: s.claim.Revision,
			LeaseToken: s.claim.LeaseToken, Payload: append([]byte(nil), payload...),
			CreatedAt: now, UpdatedAt: now,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"job_id": row.JobID, "revision": row.Revision, "lease_token": row.LeaseToken,
				"payload": row.Payload, "updated_at": now,
			}),
		}).Create(&row).Error
	})
}

// Delete removes a checkpoint only when this exact lease terminalized the job.
// A recovered worker may delete the predecessor checkpoint without first
// adopting it; the predecessor cannot delete after the recovered lease wins.
func (s *BoundAgentCheckpointStore) Delete(ctx context.Context, checkpointID string) error {
	if s == nil || s.store == nil {
		return errors.New("agent checkpoint store is unavailable")
	}
	checkpointID, err := normalizeAgentCheckpointID(checkpointID)
	if err != nil {
		return err
	}
	return s.store.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row agentCheckpointRow
		err := tx.Where("id = ?", checkpointID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.JobID != s.claim.JobID || row.Revision != s.claim.Revision {
			return ErrAgentCheckpointClaimMismatch
		}
		result := tx.Model(&conversationJobRow{}).
			Where("id = ? AND revision = ? AND terminal_lease_token = ? AND state IN ?",
				s.claim.JobID, s.claim.Revision, s.claim.LeaseToken, conversationJobStateStrings([]ConversationJobState{
					ConversationJobStateCompleted, ConversationJobStateFailed,
					ConversationJobStateExpired, ConversationJobStateCancelled,
				})).UpdateColumn("updated_at", gorm.Expr("updated_at"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgentCheckpointClaimMismatch
		}
		result = tx.Where("id = ? AND job_id = ? AND revision = ?",
			checkpointID, s.claim.JobID, s.claim.Revision).Delete(&agentCheckpointRow{})
		return result.Error
	})
}

func (s *BoundAgentCheckpointStore) rowMatchesClaim(row agentCheckpointRow) bool {
	return row.JobID == s.claim.JobID && row.Revision == s.claim.Revision && row.LeaseToken == s.claim.LeaseToken
}

func deleteAgentCheckpointsForJobs(tx *gorm.DB, jobIDs []int64) error {
	jobIDs = uniqueInt64s(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	return tx.Where("job_id IN ?", jobIDs).Delete(&agentCheckpointRow{}).Error
}

func normalizeAgentCheckpointID(checkpointID string) (string, error) {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return "", errors.New("agent checkpoint id is empty")
	}
	return checkpointID, nil
}
