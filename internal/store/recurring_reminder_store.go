package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"gorm.io/gorm"
)

const RecurringReminderFreshness = 5 * time.Minute

var ErrRecurringReminderNotFound = errors.New("找不到这条周期提醒")
var ErrRecurringReminderEnded = errors.New("截止日期内没有未来的提醒时间，请修改截止日期")

type RecurringReminder struct {
	ID int64 `json:"id"`
	RecurringReminderSpec
	Paused   bool       `json:"paused"`
	Revision uint64     `json:"revision"`
	NextAt   *time.Time `json:"nextAt"`
}

type recurringReminderRow struct {
	ID               int64      `gorm:"primaryKey"`
	UserID           int64      `gorm:"not null;index:idx_recurring_reminders_owner"`
	Platform         string     `gorm:"not null"`
	ExternalUserID   string     `gorm:"not null"`
	ConversationType string     `gorm:"not null"`
	ConversationID   string     `gorm:"not null"`
	Text             string     `gorm:"not null"`
	Frequency        string     `gorm:"not null"`
	Weekday          int        `gorm:"not null"`
	TimeOfDay        string     `gorm:"not null"`
	Timezone         string     `gorm:"not null"`
	EndDate          string     `gorm:"not null"`
	Paused           bool       `gorm:"not null;index:idx_recurring_reminders_due,priority:1"`
	Revision         uint64     `gorm:"not null"`
	NextAt           *time.Time `gorm:"index:idx_recurring_reminders_due,priority:2"`
}

func (recurringReminderRow) TableName() string { return "recurring_reminders" }

func recurringReminder(row recurringReminderRow) RecurringReminder {
	return RecurringReminder{ID: row.ID, RecurringReminderSpec: RecurringReminderSpec{
		Text: row.Text, Frequency: row.Frequency, Weekday: time.Weekday(row.Weekday), TimeOfDay: row.TimeOfDay, Timezone: row.Timezone, EndDate: row.EndDate,
	}, Paused: row.Paused, Revision: row.Revision, NextAt: row.NextAt}
}

func recurringReminderOwner(ident Identity) (Identity, error) {
	ident = normalizeIdentity(ident)
	if !IsDirectConversation(ident) {
		return ident, errors.New("周期提醒只能在私聊中管理")
	}
	return ident, validateConversationIdentity(ident)
}

func findRecurringReminder(tx *gorm.DB, ident Identity, id int64) (recurringReminderRow, error) {
	var row recurringReminderRow
	err := tx.Where("id = ? AND platform = ? AND external_user_id = ?", id, ident.Platform, ident.UserID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrRecurringReminderNotFound
	}
	return row, err
}

func (s *Store) RecurringReminders(ctx context.Context, ident Identity) ([]RecurringReminder, error) {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return nil, err
	}
	var rows []recurringReminderRow
	if err := s.db.WithContext(ctx).Where("platform = ? AND external_user_id = ?", ident.Platform, ident.UserID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]RecurringReminder, 0, len(rows))
	for _, row := range rows {
		result = append(result, recurringReminder(row))
	}
	return result, nil
}

func (s *Store) RecurringReminder(ctx context.Context, ident Identity, id int64) (RecurringReminder, error) {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return RecurringReminder{}, err
	}
	row, err := findRecurringReminder(s.db.WithContext(ctx), ident, id)
	return recurringReminder(row), err
}

// SaveRecurringReminder creates when id is zero, otherwise replaces the owned
// rule. An edit keeps its paused state and invalidates outstanding old content.
func (s *Store) SaveRecurringReminder(ctx context.Context, ident Identity, id int64, spec RecurringReminderSpec, now time.Time) (RecurringReminder, error) {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return RecurringReminder{}, err
	}
	spec, err = normalizeRecurringReminderSpec(spec)
	if err != nil {
		return RecurringReminder{}, err
	}
	next, err := nextRecurringReminderAt(spec, now)
	if err != nil {
		return RecurringReminder{}, err
	}
	if next == nil {
		return RecurringReminder{}, ErrRecurringReminderEnded
	}
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return RecurringReminder{}, err
	}
	var row recurringReminderRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if id != 0 {
			var err error
			row, err = findRecurringReminder(tx, ident, id)
			if err != nil {
				return err
			}
			if recurringReminder(row).RecurringReminderSpec == spec {
				return nil
			}
			if err := expireRecurringMessages(tx, row.ID); err != nil {
				return err
			}
		} else {
			row = recurringReminderRow{UserID: userID, Platform: ident.Platform, ExternalUserID: ident.UserID, ConversationType: ident.ConversationType, ConversationID: ident.ConversationID}
		}
		row.Text, row.Frequency, row.Weekday = spec.Text, spec.Frequency, int(spec.Weekday)
		row.TimeOfDay, row.Timezone, row.EndDate = spec.TimeOfDay, spec.Timezone, spec.EndDate
		row.Revision++
		row.NextAt = next
		if row.Paused {
			row.NextAt = nil
		}
		return tx.Save(&row).Error
	})
	return recurringReminder(row), err
}

func expireRecurringMessages(tx *gorm.DB, id int64) error {
	return tx.Model(&outgoingMessageRow{}).Where("kind = ? AND dedupe_key LIKE ? AND status IN ?", "notification.recurring", fmt.Sprintf("recurring:%d:%%", id), []string{string(delivery.StatusPending), string(delivery.StatusRetryWait), string(delivery.StatusDelivering)}).
		Updates(map[string]any{"status": string(delivery.StatusExpired), "updated_at": nowUTC()}).Error
}

func (s *Store) PauseRecurringReminder(ctx context.Context, ident Identity, id int64, paused bool, now time.Time) (RecurringReminder, error) {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return RecurringReminder{}, err
	}
	var row recurringReminderRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		row, err = findRecurringReminder(tx, ident, id)
		if err != nil || row.Paused == paused {
			return err
		}
		row.NextAt = nil
		if !paused {
			row.NextAt, err = nextRecurringReminderAt(recurringReminder(row).RecurringReminderSpec, now)
			if err != nil {
				return err
			}
			if row.NextAt == nil {
				return ErrRecurringReminderEnded
			}
		}
		if err := expireRecurringMessages(tx, id); err != nil {
			return err
		}
		row.Paused, row.Revision = paused, row.Revision+1
		return tx.Save(&row).Error
	})
	return recurringReminder(row), err
}

func (s *Store) DeleteRecurringReminder(ctx context.Context, ident Identity, id int64) error {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := findRecurringReminder(tx, ident, id)
		if err != nil {
			return err
		}
		if err := expireRecurringMessages(tx, row.ID); err != nil {
			return err
		}
		return tx.Delete(&row).Error
	})
}

// PauseAllRecurringReminders implements the existing "通知 关" promise while
// retaining opt-in rule definitions for a later explicit resume.
func (s *Store) PauseAllRecurringReminders(ctx context.Context, ident Identity) error {
	ident, err := recurringReminderOwner(ident)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []recurringReminderRow
		if err := tx.Where("platform = ? AND external_user_id = ? AND paused = ?", ident.Platform, ident.UserID, false).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := expireRecurringMessages(tx, row.ID); err != nil {
				return err
			}
			row.Paused, row.NextAt, row.Revision = true, nil, row.Revision+1
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// EnqueueDueRecurringReminders advances the rule and inserts the existing
// durable outbox in one transaction. Restarts never replay a model operation.
func (s *Store) EnqueueDueRecurringReminders(ctx context.Context, now time.Time) error {
	now = now.UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []recurringReminderRow
		if err := tx.Where("paused = ? AND next_at <= ?", false, now).Order("next_at, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			spec := recurringReminder(row).RecurringReminderSpec
			next, err := nextRecurringReminderAt(spec, now)
			if err != nil {
				return err
			}
			// A stale watermark may precede today's still-fresh occurrence. Find
			// the window's occurrence instead of replaying or merely skipping it.
			fresh, err := nextRecurringReminderAt(spec, now.Add(-RecurringReminderFreshness))
			if err != nil {
				return err
			}
			if fresh != nil && !fresh.After(now) && !fresh.Before(*row.NextAt) {
				scheduled := *fresh
				location, err := time.LoadLocation(row.Timezone)
				if err != nil {
					return err
				}
				text := fmt.Sprintf("周期提醒：\n%s\n%s · %s", row.Text, scheduled.In(location).Format("2006-01-02 15:04"), row.Timezone)
				outbound := message.Outbound{Kind: "notification.recurring", TextPolicy: message.TextPolicyImageOnly,
					Target:    message.Conversation{Platform: row.Platform, Type: row.ConversationType, ID: row.ConversationID},
					Content:   message.Content{Parts: []message.ContentPart{{Text: text}}},
					DedupeKey: fmt.Sprintf("recurring:%d:%d:%s", row.ID, row.Revision, scheduled.Format(time.RFC3339)), ExpiresAt: scheduled.Add(RecurringReminderFreshness),
				}
				if _, _, err := enqueueWithDB(tx, outbound, now); err != nil {
					return err
				}
			}
			if err := tx.Model(&row).Update("next_at", next).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
