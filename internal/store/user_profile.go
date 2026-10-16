package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SaveDirectUserDisplayName retains the latest nonempty name observed in a
// direct message. Group cards are conversation-specific and never belong here.
func (s *Store) SaveDirectUserDisplayName(ctx context.Context, ident Identity, displayName string, seenAt time.Time) error {
	if err := validateIdentity(ident); err != nil {
		return err
	}
	ident = normalizeIdentity(ident)
	if !IsDirectConversation(ident) {
		return errors.New("user display name requires a direct conversation")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return nil
	}
	if seenAt.IsZero() {
		seenAt = nowUTC()
	}
	seenAt = seenAt.UTC()
	now := nowUTC()
	user := userRow{
		Platform: ident.Platform, ExternalUserID: ident.UserID,
		DisplayName: displayName, DisplayNameSeenAt: &seenAt,
		CreatedAt: now, UpdatedAt: now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "platform"}, {Name: "external_user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"display_name": displayName, "display_name_seen_at": seenAt, "updated_at": now,
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "users.display_name_seen_at IS NULL OR users.display_name_seen_at <= ?", Vars: []any{seenAt}},
		}},
	}).Create(&user).Error
}

func backfillDirectUserDisplayNames(tx *gorm.DB) error {
	type observation struct {
		Platform       string
		ExternalUserID string
		DisplayName    string
		SeenAt         time.Time
	}
	var observations []observation
	if err := tx.Raw(`SELECT platform, external_user_id, display_name, seen_at FROM (
		SELECT platform, external_user_id, TRIM(COALESCE(actor_display_name, '')) AS display_name,
			created_at AS seen_at
		FROM conversation_events
		WHERE type = 'user' AND conversation_type IN ('private', 'guild_private')
		UNION ALL
		SELECT platform, external_user_id,
			TRIM(COALESCE(CASE WHEN json_valid(input_json)
				THEN json_extract(input_json, '$.data.inbound.Actor.DisplayName') END, '')) AS display_name,
			created_at AS seen_at
		FROM conversation_jobs
		WHERE conversation_type IN ('private', 'guild_private')
	) WHERE display_name <> ''`).Scan(&observations).Error; err != nil {
		return fmt.Errorf("read direct display names for backfill: %w", err)
	}
	type key struct{ platform, userID string }
	latest := make(map[key]observation)
	for _, item := range observations {
		identifier := key{item.Platform, item.ExternalUserID}
		if previous, exists := latest[identifier]; !exists || !item.SeenAt.Before(previous.SeenAt) {
			latest[identifier] = item
		}
	}
	for identifier, item := range latest {
		if err := tx.Model(&userRow{}).
			Where("platform = ? AND external_user_id = ?", identifier.platform, identifier.userID).
			UpdateColumns(map[string]any{
				"display_name": item.DisplayName, "display_name_seen_at": item.SeenAt,
			}).Error; err != nil {
			return fmt.Errorf("backfill direct display name: %w", err)
		}
	}
	return nil
}
