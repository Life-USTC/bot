package store

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/responses"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// commandImageRow stores one immutable rendered image specification for a
// command execution. The scoped key is supplied by the command workflow and
// normally includes its business execution identifier and image index.
type commandImageRow struct {
	ID               string `gorm:"primaryKey"`
	Platform         string `gorm:"not null;uniqueIndex:idx_command_images_scope_key,priority:1"`
	UserID           string `gorm:"not null;uniqueIndex:idx_command_images_scope_key,priority:2"`
	ConversationType string `gorm:"not null;uniqueIndex:idx_command_images_scope_key,priority:3"`
	ConversationID   string `gorm:"not null;uniqueIndex:idx_command_images_scope_key,priority:4"`
	Key              string `gorm:"not null;uniqueIndex:idx_command_images_scope_key,priority:5"`
	ImageJSON        string `gorm:"not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (commandImageRow) TableName() string { return "command_images" }

const commandImageInsertAttempts = 8

// SaveCommandImage stores an image specification under a key scoped to the
// exact actor and conversation. A key is immutable: repeated saves return the
// original row's ID without replacing its image, even when the new image is
// different.
func (s *Store) SaveCommandImage(ctx context.Context, ident Identity, key string, image *responses.Image) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("store is unavailable")
	}
	if err := validateConversationIdentity(ident); err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("command image key is empty")
	}
	if image == nil {
		return "", errors.New("command image is nil")
	}
	imageJSON, err := json.Marshal(image)
	if err != nil {
		return "", fmt.Errorf("encode command image: %w", err)
	}
	ident = normalizeIdentity(ident)
	now := nowUTC()

	for attempt := 0; attempt < commandImageInsertAttempts; attempt++ {
		id, err := newCommandImageID()
		if err != nil {
			return "", err
		}
		row := commandImageRow{
			ID:               id,
			Platform:         ident.Platform,
			UserID:           ident.UserID,
			ConversationType: ident.ConversationType,
			ConversationID:   ident.ConversationID,
			Key:              key,
			ImageJSON:        string(imageJSON),
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return "", result.Error
		}

		var existing commandImageRow
		err = s.db.WithContext(ctx).Where(commandImageScopeKeyQuery(ident, key)).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A no-op insert can also mean the random ID collided with an
			// unrelated row. Retry with another opaque ID in that unlikely case.
			continue
		}
		if err != nil {
			return "", err
		}
		return existing.ID, nil
	}
	return "", errors.New("generate unique command image id")
}

// CommandImage resolves an image only inside the exact actor and
// conversation scope that saved it. The found result remains true when the
// row exists but its persisted JSON is malformed, so callers can distinguish
// corruption from a missing reference.
func (s *Store) CommandImage(ctx context.Context, ident Identity, id string) (*responses.Image, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("store is unavailable")
	}
	if err := validateConversationIdentity(ident); err != nil {
		return nil, false, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false, errors.New("command image id is empty")
	}
	ident = normalizeIdentity(ident)

	var row commandImageRow
	err := s.db.WithContext(ctx).
		Where(commandImageIdentityIDQuery(ident, id)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var image *responses.Image
	if err := json.Unmarshal([]byte(row.ImageJSON), &image); err != nil {
		return nil, true, fmt.Errorf("decode command image %s: %w", id, err)
	}
	if image == nil {
		return nil, true, fmt.Errorf("decode command image %s: image spec is null", id)
	}
	return image, true, nil
}

func newCommandImageID() (string, error) {
	value := cryptorand.Text()
	if value == "" {
		return "", errors.New("generate command image id: random source returned an empty value")
	}
	return "img_" + value, nil
}

func commandImageScopeKeyQuery(ident Identity, key string) map[string]any {
	return map[string]any{
		"platform":          ident.Platform,
		"user_id":           ident.UserID,
		"conversation_type": ident.ConversationType,
		"conversation_id":   ident.ConversationID,
		"key":               key,
	}
}

func commandImageIdentityIDQuery(ident Identity, id string) map[string]any {
	return map[string]any{
		"id":                id,
		"platform":          ident.Platform,
		"user_id":           ident.UserID,
		"conversation_type": ident.ConversationType,
		"conversation_id":   ident.ConversationID,
	}
}
