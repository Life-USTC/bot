package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConversationCompaction is the committed, model-readable summary for one
// conversation. The raw conversation_events remain the source of truth; this
// row only records the prefix that has been compressed for a future model
// request.
type ConversationCompaction struct {
	ID             int64
	Identity       Identity
	CoveredEventID int64
	Summary        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var (
	// ErrConversationCompactionClaimMismatch means that another worker owns the
	// compaction row, or that the row has advanced since the claim was made.
	ErrConversationCompactionClaimMismatch = errors.New("conversation compaction claim does not match")
	// ErrConversationCompactionClaimRequired prevents an empty token from
	// accidentally committing a compaction created by another worker.
	ErrConversationCompactionClaimRequired = errors.New("conversation compaction claim token is required")
)

type conversationCompactionRow struct {
	ID               int64      `gorm:"primaryKey"`
	Platform         string     `gorm:"not null;uniqueIndex:idx_conversation_compactions_identity,priority:1"`
	ConversationType string     `gorm:"not null;uniqueIndex:idx_conversation_compactions_identity,priority:2"`
	ConversationID   string     `gorm:"not null;uniqueIndex:idx_conversation_compactions_identity,priority:3"`
	CoveredEventID   int64      `gorm:"not null;default:0"`
	Summary          string     `gorm:"not null"`
	ClaimToken       string     `gorm:"not null;default:''"`
	ClaimExpiresAt   *time.Time `gorm:"index"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (conversationCompactionRow) TableName() string { return "conversation_compactions" }

// EnsureConversationCompactionSchema explicitly creates the current
// compaction table. It is intentionally separate from Store.Open: production
// deployment runs the maintenance command while the bot is quiesced, instead
// of silently changing a live database during startup.
func (s *Store) EnsureConversationCompactionSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	if s.db.WithContext(ctx).Migrator().HasTable(&conversationCompactionRow{}) {
		return verifyModelSchema(s.db.WithContext(ctx), &conversationCompactionRow{})
	}
	return s.db.WithContext(ctx).Migrator().CreateTable(&conversationCompactionRow{})
}

// ConversationCompaction returns the committed summary row for a
// conversation. A claimed but not yet committed row is returned with an empty
// Summary, which lets the caller keep using raw history without observing a
// partial summary.
func (s *Store) ConversationCompaction(ctx context.Context, ident Identity) (ConversationCompaction, bool, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return ConversationCompaction{}, false, err
	}
	if s == nil || s.db == nil {
		return ConversationCompaction{}, false, errors.New("store is unavailable")
	}
	ident = normalizeIdentity(ident)
	var row conversationCompactionRow
	err := s.db.WithContext(ctx).Where(compactionIdentityQuery(ident)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ConversationCompaction{}, false, nil
	}
	if err != nil {
		return ConversationCompaction{}, false, err
	}
	return conversationCompactionFromRow(row), true, nil
}

// ClaimConversationCompaction claims the next prefix only when the committed
// cursor still equals expectedCoveredEventID. The conditional update is the
// concurrency boundary: a second worker either observes the active lease or a
// newer cursor and must leave the row untouched.
func (s *Store) ClaimConversationCompaction(ctx context.Context, ident Identity, expectedCoveredEventID int64, token string, expiresAt time.Time) (bool, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return false, err
	}
	if s == nil || s.db == nil {
		return false, errors.New("store is unavailable")
	}
	if expectedCoveredEventID < 0 {
		return false, errors.New("conversation compaction cursor is invalid")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false, ErrConversationCompactionClaimRequired
	}
	if expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		return false, errors.New("conversation compaction claim expiry is invalid")
	}
	ident = normalizeIdentity(ident)
	now := nowUTC()
	expiresAt = expiresAt.UTC()

	var claimed bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A non-zero cursor proves that a committed row must already exist.
		// Creating one at that cursor would skip the raw prefix after a
		// database reset or a partially deployed schema.
		if expectedCoveredEventID == 0 {
			row := conversationCompactionRow{
				Platform:         ident.Platform,
				ConversationType: ident.ConversationType,
				ConversationID:   ident.ConversationID,
				CoveredEventID:   0,
				Summary:          "",
				ClaimToken:       token,
				ClaimExpiresAt:   &expiresAt,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				claimed = true
				return nil
			}
		}

		result := tx.Model(&conversationCompactionRow{}).
			Where(compactionIdentityQuery(ident)).
			Where("covered_event_id = ?", expectedCoveredEventID).
			Where("claim_token = '' OR claim_expires_at IS NULL OR claim_expires_at <= ?", now).
			Updates(map[string]any{"claim_token": token, "claim_expires_at": expiresAt, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// RenewConversationCompactionClaim keeps a live summary request's lease fresh
// without changing the committed summary or its covered event cursor.
func (s *Store) RenewConversationCompactionClaim(ctx context.Context, ident Identity, expectedCoveredEventID int64, token string, expiresAt time.Time) (bool, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return false, err
	}
	if strings.TrimSpace(token) == "" {
		return false, ErrConversationCompactionClaimRequired
	}
	now := nowUTC()
	if expectedCoveredEventID < 0 || !expiresAt.After(now) {
		return false, errors.New("invalid conversation compaction lease renewal")
	}
	result := s.db.WithContext(ctx).Model(&conversationCompactionRow{}).
		Where(compactionIdentityQuery(normalizeIdentity(ident))).
		Where("covered_event_id = ? AND claim_token = ? AND claim_expires_at > ?", expectedCoveredEventID, token, now).
		Updates(map[string]any{"claim_expires_at": expiresAt.UTC(), "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

// CommitConversationCompaction atomically publishes the summary and advances
// the covered event cursor. Raw events are never deleted, and an expired or
// stale worker cannot publish its model output.
func (s *Store) CommitConversationCompaction(ctx context.Context, ident Identity, expectedCoveredEventID, coveredEventID int64, token, summary string) error {
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	if expectedCoveredEventID < 0 || coveredEventID <= expectedCoveredEventID {
		return errors.New("conversation compaction cursor transition is invalid")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrConversationCompactionClaimRequired
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("conversation compaction summary is empty")
	}
	ident = normalizeIdentity(ident)
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&conversationCompactionRow{}).
		Where(compactionIdentityQuery(ident)).
		Where("covered_event_id = ? AND claim_token = ? AND claim_expires_at > ?", expectedCoveredEventID, token, now).
		Updates(map[string]any{
			"covered_event_id": coveredEventID,
			"summary":          summary,
			"claim_token":      "",
			"claim_expires_at": nil,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConversationCompactionClaimMismatch
	}
	return nil
}

// ReleaseConversationCompaction clears a failed worker's claim without
// changing its committed cursor or summary. Expiration still recovers a
// process that died before releasing its claim.
func (s *Store) ReleaseConversationCompaction(ctx context.Context, ident Identity, expectedCoveredEventID int64, token string) error {
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrConversationCompactionClaimRequired
	}
	if expectedCoveredEventID < 0 {
		return errors.New("conversation compaction cursor is invalid")
	}
	ident = normalizeIdentity(ident)
	result := s.db.WithContext(ctx).Model(&conversationCompactionRow{}).
		Where(compactionIdentityQuery(ident)).
		Where("covered_event_id = ? AND claim_token = ?", expectedCoveredEventID, token).
		Updates(map[string]any{"claim_token": "", "claim_expires_at": nil, "updated_at": nowUTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConversationCompactionClaimMismatch
	}
	return nil
}

func compactionIdentityQuery(ident Identity) map[string]any {
	return map[string]any{
		"platform":          ident.Platform,
		"conversation_type": ident.ConversationType,
		"conversation_id":   ident.ConversationID,
	}
}

func conversationCompactionFromRow(row conversationCompactionRow) ConversationCompaction {
	return ConversationCompaction{
		ID:             row.ID,
		Identity:       Identity{Platform: row.Platform, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		CoveredEventID: row.CoveredEventID,
		Summary:        row.Summary,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
