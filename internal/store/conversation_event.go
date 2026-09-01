package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConversationEventType is the exact role-bearing transcript consumed by the
// model. Host approvals, progress messages and receipts are intentionally not
// event types and therefore cannot leak into future model context.
type ConversationEventType string

const (
	ConversationEventUser       ConversationEventType = "user"
	ConversationEventAssistant  ConversationEventType = "assistant"
	ConversationEventToolResult ConversationEventType = "tool_result"
	ConversationEventToolError  ConversationEventType = "tool_error"
	ConversationEventToolDenial ConversationEventType = "tool_denial"
)

type ConversationToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type,omitempty"`
	Index     *int   `json:"index,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ConversationMessagePart is the durable subset of Eino's typed message
// parts. Text and image_url are the parts currently supported by this
// application. Provider-only reasoning and other transient part kinds are
// intentionally not represented here.
type ConversationMessagePart struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	URL        string `json:"url,omitempty"`
	Reference  string `json:"reference,omitempty"`
	Base64Data string `json:"base64data,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type ConversationEvent struct {
	ID         int64
	Identity   Identity
	JobID      int64
	DedupeKey  string
	Type       ConversationEventType
	Content    string
	Name       string
	ToolCallID string
	ToolName   string
	ToolCalls  []ConversationToolCall
	Parts      []ConversationMessagePart
	CreatedAt  time.Time
}

type conversationEventRow struct {
	ID               int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null;index:idx_conversation_events_identity_id,priority:1;uniqueIndex:idx_conversation_events_dedupe,priority:1"`
	ConversationType string `gorm:"not null;index:idx_conversation_events_identity_id,priority:2"`
	ConversationID   string `gorm:"not null;index:idx_conversation_events_identity_id,priority:3"`
	ExternalUserID   string `gorm:"not null"`
	JobID            int64  `gorm:"not null;default:0;index"`
	DedupeKey        string `gorm:"not null;uniqueIndex:idx_conversation_events_dedupe,priority:2"`
	Type             string `gorm:"not null"`
	Content          string
	Name             string
	ToolCallID       string
	ToolName         string
	ToolCallsJSON    string    `gorm:"not null;default:'[]'"`
	PartsJSON        string    `gorm:"not null;default:'[]'"`
	CreatedAt        time.Time `gorm:"index:idx_conversation_events_identity_id,priority:4"`
}

const maxConversationMessagePartsJSONBytes = 32 << 20

func (conversationEventRow) TableName() string { return "conversation_events" }

// AppendConversationEvent is idempotent on a caller-owned key. It returns
// created=false with the original event when a lease retry replays the same
// model or tool event.
func (s *Store) AppendConversationEvent(ctx context.Context, event ConversationEvent) (ConversationEvent, bool, error) {
	if err := validateConversationIdentity(event.Identity); err != nil {
		return ConversationEvent{}, false, err
	}
	event.Identity = normalizeIdentity(event.Identity)
	event.DedupeKey = strings.TrimSpace(event.DedupeKey)
	if event.DedupeKey == "" {
		return ConversationEvent{}, false, errors.New("conversation event dedupe key is empty")
	}
	if !validConversationEventType(event.Type) {
		return ConversationEvent{}, false, fmt.Errorf("invalid conversation event type %q", event.Type)
	}
	toolCallsJSON, err := json.Marshal(event.ToolCalls)
	if err != nil {
		return ConversationEvent{}, false, fmt.Errorf("encode conversation tool calls: %w", err)
	}
	partsJSON, err := json.Marshal(event.Parts)
	if err != nil {
		return ConversationEvent{}, false, fmt.Errorf("encode conversation message parts: %w", err)
	}
	if string(partsJSON) == "null" {
		partsJSON = []byte("[]")
	}
	if len(partsJSON) > maxConversationMessagePartsJSONBytes {
		return ConversationEvent{}, false, fmt.Errorf("conversation message parts exceed %d bytes", maxConversationMessagePartsJSONBytes)
	}
	createdAt := event.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = nowUTC()
	}
	row := conversationEventRow{
		Platform: event.Identity.Platform, ConversationType: event.Identity.ConversationType,
		ConversationID: event.Identity.ConversationID, ExternalUserID: event.Identity.UserID,
		JobID: event.JobID, DedupeKey: event.DedupeKey, Type: string(event.Type), Content: event.Content,
		Name:       strings.TrimSpace(event.Name),
		ToolCallID: strings.TrimSpace(event.ToolCallID), ToolName: strings.TrimSpace(event.ToolName),
		ToolCallsJSON: string(toolCallsJSON), PartsJSON: string(partsJSON), CreatedAt: createdAt,
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return ConversationEvent{}, false, result.Error
	}
	created := result.RowsAffected == 1
	if !created {
		if err := s.db.WithContext(ctx).
			Where("platform = ? AND dedupe_key = ?", event.Identity.Platform, event.DedupeKey).
			First(&row).Error; err != nil {
			return ConversationEvent{}, false, err
		}
	}
	saved, err := conversationEventFromRow(row)
	return saved, created, err
}

func (s *Store) RecentConversationEvents(ctx context.Context, ident Identity, limit int) ([]ConversationEvent, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	ident = normalizeIdentity(ident)
	var rows []conversationEventRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID).
		Order("id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	events := make([]ConversationEvent, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		event, err := conversationEventFromRow(rows[i])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func conversationEventFromRow(row conversationEventRow) (ConversationEvent, error) {
	var calls []ConversationToolCall
	if err := json.Unmarshal([]byte(row.ToolCallsJSON), &calls); err != nil {
		return ConversationEvent{}, fmt.Errorf("decode conversation event %d tool calls: %w", row.ID, err)
	}
	var parts []ConversationMessagePart
	if strings.TrimSpace(row.PartsJSON) != "" {
		if len(row.PartsJSON) > maxConversationMessagePartsJSONBytes {
			return ConversationEvent{}, fmt.Errorf("conversation event %d message parts exceed %d bytes", row.ID, maxConversationMessagePartsJSONBytes)
		}
		if err := json.Unmarshal([]byte(row.PartsJSON), &parts); err != nil {
			return ConversationEvent{}, fmt.Errorf("decode conversation event %d message parts: %w", row.ID, err)
		}
	}
	return ConversationEvent{
		ID:       row.ID,
		Identity: Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		JobID:    row.JobID, DedupeKey: row.DedupeKey, Type: ConversationEventType(row.Type), Content: row.Content,
		Name: row.Name, ToolCallID: row.ToolCallID, ToolName: row.ToolName, ToolCalls: calls, Parts: parts, CreatedAt: row.CreatedAt,
	}, nil
}

func validConversationEventType(eventType ConversationEventType) bool {
	switch eventType {
	case ConversationEventUser, ConversationEventAssistant, ConversationEventToolResult,
		ConversationEventToolError, ConversationEventToolDenial:
		return true
	default:
		return false
	}
}

// migrateLegacyConversationEvents keeps the exact legacy user/assistant text
// but deliberately discards generated summaries. Legacy rows never claimed to
// contain typed tool calls, so none are invented during migration.
func migrateLegacyConversationEvents(tx *gorm.DB) error {
	var rows []interactionRow
	if err := tx.Where("direction = ? AND handled = ? AND status = ?",
		InteractionDirectionInbound, true, InteractionStatusHandled).
		Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	for _, interaction := range rows {
		ident := Identity{
			Platform: interaction.Platform, UserID: interaction.UserID,
			ConversationType: interaction.ConversationType, ConversationID: interaction.ConversationID,
		}
		for _, event := range []ConversationEvent{
			{Identity: ident, DedupeKey: fmt.Sprintf("legacy:%d:user", interaction.ID), Type: ConversationEventUser, Content: interaction.RawText, CreatedAt: interaction.CreatedAt},
			{Identity: ident, DedupeKey: fmt.Sprintf("legacy:%d:assistant", interaction.ID), Type: ConversationEventAssistant, Content: interaction.Reply, CreatedAt: interaction.CreatedAt},
		} {
			if strings.TrimSpace(event.Content) == "" {
				continue
			}
			calls, _ := json.Marshal(event.ToolCalls)
			row := conversationEventRow{
				Platform: ident.Platform, ConversationType: ident.ConversationType, ConversationID: ident.ConversationID,
				ExternalUserID: ident.UserID, DedupeKey: event.DedupeKey, Type: string(event.Type), Content: event.Content,
				ToolCallsJSON: string(calls), PartsJSON: "[]", CreatedAt: event.CreatedAt,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
