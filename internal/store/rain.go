package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RainSettings is independent of OAuth-backed personal reminders: weather is public.
type RainSettings struct {
	Identity Identity
	Enabled  bool
	Campus   string
	Revision uint64
}

type rainSettingRow struct {
	UserID           int64  `gorm:"primaryKey;autoIncrement:false"`
	Platform         string `gorm:"not null"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string `gorm:"not null"`
	ConversationID   string `gorm:"not null"`
	Enabled          bool   `gorm:"not null"`
	Campus           string `gorm:"not null"`
	Revision         uint64 `gorm:"not null"`
	Session          string `gorm:"not null"`
	ObservedAt       *time.Time
	Raining          bool `gorm:"not null"`
}

func (rainSettingRow) TableName() string { return "rain_settings" }

func rainSettings(row rainSettingRow) RainSettings {
	return RainSettings{Identity: Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID}, Enabled: row.Enabled, Campus: row.Campus, Revision: row.Revision}
}
func (s *Store) RainSettings(ctx context.Context, ident Identity) (RainSettings, error) {
	if err := validateIdentity(ident); err != nil {
		return RainSettings{}, err
	}
	ident = normalizeIdentity(ident)
	var row rainSettingRow
	err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", ident.Platform, ident.UserID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RainSettings{Identity: ident, Campus: "ustc-main"}, nil
	}
	return rainSettings(row), err
}
func (s *Store) SaveRainSettings(ctx context.Context, settings RainSettings) error {
	settings.Identity = normalizeIdentity(settings.Identity)
	if !IsDirectConversation(settings.Identity) {
		return errors.New("rain reminders require a direct conversation")
	}
	if err := validateConversationIdentity(settings.Identity); err != nil {
		return err
	}
	if settings.Campus != "ustc-main" && settings.Campus != "ustc-gaoxin" {
		return errors.New("unknown rain reminder campus")
	}
	userID, err := s.EnsureUser(ctx, settings.Identity)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous rainSettingRow
		err := tx.Where("user_id = ?", userID).Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && previous.Enabled == settings.Enabled && previous.Campus == settings.Campus && rainSettings(previous).Identity == settings.Identity {
			return nil
		}
		if err == nil {
			if err := expireRainMessages(tx, rainSettings(previous).Identity); err != nil {
				return err
			}
		}
		row := rainSettingRow{UserID: userID, Platform: settings.Identity.Platform, ExternalUserID: settings.Identity.UserID, ConversationType: settings.Identity.ConversationType, ConversationID: settings.Identity.ConversationID, Enabled: settings.Enabled, Campus: settings.Campus, Revision: previous.Revision + 1}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
}
func (s *Store) EnabledRainSettings(ctx context.Context) ([]RainSettings, error) {
	var rows []rainSettingRow
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RainSettings, 0, len(rows))
	for _, row := range rows {
		out = append(out, rainSettings(row))
	}
	return out, nil
}
func expireRainMessages(tx *gorm.DB, ident Identity) error {
	return tx.Model(&outgoingMessageRow{}).Where("kind = ? AND platform = ? AND conversation_type = ? AND conversation_id = ? AND status IN ?", "notification.rain", ident.Platform, ident.ConversationType, ident.ConversationID, []string{string(delivery.StatusPending), string(delivery.StatusRetryWait), string(delivery.StatusDelivering)}).Updates(map[string]any{"status": string(delivery.StatusExpired), "updated_at": nowUTC()}).Error
}

// ObserveRain commits the state and outbox together. An empty session invalidates the
// baseline without moving its timestamp backwards; the next valid read establishes it.
func (s *Store) ObserveRain(ctx context.Context, settings RainSettings, session string, observedAt time.Time, raining bool, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row rainSettingRow
		if err := tx.Where("platform = ? AND external_user_id = ?", settings.Identity.Platform, settings.Identity.UserID).Take(&row).Error; err != nil {
			return err
		}
		if !row.Enabled || row.Revision != settings.Revision || !IsDirectConversation(rainSettings(row).Identity) {
			return nil
		}
		if session == "" {
			if err := expireRainMessages(tx, settings.Identity); err != nil {
				return err
			}
			return tx.Model(&row).Update("session", "").Error
		}
		if row.ObservedAt != nil && observedAt.Before(*row.ObservedAt) {
			if err := expireRainMessages(tx, settings.Identity); err != nil {
				return err
			}
			return tx.Model(&row).Update("session", "").Error
		}
		baseline := row.Session != session || row.ObservedAt == nil || observedAt.Sub(*row.ObservedAt) >= time.Hour
		same := row.ObservedAt != nil && observedAt.Equal(*row.ObservedAt)
		if !baseline && same {
			if raining != row.Raining { // contradictory values for one observation are not a transition
				if err := expireRainMessages(tx, settings.Identity); err != nil {
					return err
				}
				return tx.Model(&row).Update("session", "").Error
			}
			return nil
		}
		if baseline || raining != row.Raining {
			if err := expireRainMessages(tx, settings.Identity); err != nil {
				return err
			}
		}
		if !baseline && raining != row.Raining {
			campus := "本部"
			if row.Campus == "ustc-gaoxin" {
				campus = "高新校区"
			}
			change := "雨停了，可以收伞。"
			if raining {
				change = "开始下雨，出门请带伞。"
			}
			zone := time.FixedZone("Asia/Shanghai", 8*3600)
			text := fmt.Sprintf("降雨提醒 · %s\n%s\n观测时间：%s（高德实况）", campus, change, observedAt.In(zone).Format("01-02 15:04"))
			expires := now.Add(5 * time.Minute)
			if limit := observedAt.Add(time.Hour); limit.Before(expires) {
				expires = limit
			}
			outbound := message.Outbound{Kind: "notification.rain", TextPolicy: message.TextPolicyImageOnly, Target: message.Conversation{Platform: row.Platform, Type: row.ConversationType, ID: row.ConversationID}, Content: message.Content{Parts: []message.ContentPart{{Text: text}}}, DedupeKey: fmt.Sprintf("rain:%s:%s:%d:%s", row.Platform, row.ExternalUserID, row.Revision, observedAt.UTC().Format(time.RFC3339Nano)), ExpiresAt: expires}
			if _, _, err := enqueueWithDB(tx, outbound, now.UTC()); err != nil {
				return err
			}
		}
		return tx.Model(&row).Updates(map[string]any{"session": session, "observed_at": observedAt.UTC(), "raining": raining}).Error
	})
}

// Pending weather describes a pre-restart state. Clear it before workers can
// claim any records, while retaining settings and the observation watermark.
func (s *Store) resetRainAfterStartup() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&rainSettingRow{}).Where("session <> ?", "").Update("session", "").Error; err != nil {
			return err
		}
		return tx.Model(&outgoingMessageRow{}).Where("kind = ? AND status IN ?", "notification.rain", []string{string(delivery.StatusPending), string(delivery.StatusRetryWait), string(delivery.StatusDelivering)}).Updates(map[string]any{"status": string(delivery.StatusExpired), "updated_at": nowUTC()}).Error
	})
}
