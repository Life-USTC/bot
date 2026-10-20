package store

import (
	"context"
	"errors"
	"strings"
	"time"

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
