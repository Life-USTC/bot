package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConversationEventType is the exact role-bearing transcript consumed by the
// model. Host approvals and receipts are intentionally not event types and
// therefore cannot leak into future model context.
type ConversationEventType string

const (
	ConversationEventUser       ConversationEventType = "user"
	ConversationEventAssistant  ConversationEventType = "assistant"
	ConversationEventToolResult ConversationEventType = "tool_result"
	ConversationEventToolError  ConversationEventType = "tool_error"
	ConversationEventToolDenial ConversationEventType = "tool_denial"
)

const (
	ConversationEventSourceAgent   = "agent"
	ConversationEventSourceCommand = "command"
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
	ID               int64
	Identity         Identity
	ActorDisplayName string
	Source           string
	OccurredAt       time.Time
	JobID            int64
	JobRevision      int
	JobLeaseToken    string
	DedupeKey        string
	Type             ConversationEventType
	Content          string
	Name             string
	ToolCallID       string
	ToolName         string
	ToolCalls        []ConversationToolCall
	Parts            []ConversationMessagePart
	CreatedAt        time.Time
}

type conversationEventRow struct {
	ID               int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null;index:idx_conversation_events_identity_id,priority:1;uniqueIndex:idx_conversation_events_dedupe,priority:1"`
	ConversationType string `gorm:"not null;index:idx_conversation_events_identity_id,priority:2"`
	ConversationID   string `gorm:"not null;index:idx_conversation_events_identity_id,priority:3"`
	ExternalUserID   string `gorm:"not null"`
	ActorDisplayName string
	Source           string
	OccurredAt       time.Time `gorm:"index"`
	JobID            int64     `gorm:"not null;default:0;index"`
	DedupeKey        string    `gorm:"not null;uniqueIndex:idx_conversation_events_dedupe,priority:2"`
	Type             string    `gorm:"not null"`
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
	event.ActorDisplayName = strings.TrimSpace(event.ActorDisplayName)
	event.Source = strings.ToLower(strings.TrimSpace(event.Source))
	if event.Source != "" && event.Source != ConversationEventSourceAgent && event.Source != ConversationEventSourceCommand {
		return ConversationEvent{}, false, fmt.Errorf("invalid conversation event source %q", event.Source)
	}
	event.JobLeaseToken = strings.TrimSpace(event.JobLeaseToken)
	if event.JobID > 0 && (event.JobRevision <= 0 || event.JobLeaseToken == "") {
		return ConversationEvent{}, false, errors.New("conversation event job claim is incomplete")
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
	occurredAt := event.OccurredAt.UTC()
	if occurredAt.IsZero() && !createdAt.IsZero() {
		occurredAt = createdAt
	}
	if createdAt.IsZero() {
		if occurredAt.IsZero() {
			createdAt = nowUTC()
		} else {
			createdAt = occurredAt
		}
	}
	name := strings.TrimSpace(event.Name)
	if name == "" && event.Type == ConversationEventUser {
		name = event.ActorDisplayName
	}
	row := conversationEventRow{
		Platform: event.Identity.Platform, ConversationType: event.Identity.ConversationType,
		ConversationID: event.Identity.ConversationID, ExternalUserID: event.Identity.UserID,
		ActorDisplayName: event.ActorDisplayName, Source: event.Source, OccurredAt: occurredAt,
		JobID: event.JobID, DedupeKey: event.DedupeKey, Type: string(event.Type), Content: event.Content,
		Name:       name,
		ToolCallID: strings.TrimSpace(event.ToolCallID), ToolName: strings.TrimSpace(event.ToolName),
		ToolCallsJSON: string(toolCallsJSON), PartsJSON: string(partsJSON), CreatedAt: createdAt,
	}
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if event.JobID > 0 {
			result := tx.Model(&conversationJobRow{}).
				Where("id = ? AND platform = ? AND conversation_type = ? AND conversation_id = ? AND external_user_id = ? AND state = ? AND revision = ? AND lease_token = ?",
					event.JobID, event.Identity.Platform, event.Identity.ConversationType, event.Identity.ConversationID, event.Identity.UserID,
					string(ConversationJobStateRunning), event.JobRevision, event.JobLeaseToken).
				UpdateColumn("updated_at", gorm.Expr("updated_at"))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("conversation event job claim does not match")
			}
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if !created {
			return tx.Where("platform = ? AND dedupe_key = ?", event.Identity.Platform, event.DedupeKey).First(&row).Error
		}
		return nil
	})
	if err != nil {
		return ConversationEvent{}, false, err
	}
	saved, err := conversationEventFromRow(row)
	return saved, created, err
}

func (s *Store) RecentConversationEvents(ctx context.Context, ident Identity, limit int) ([]ConversationEvent, error) {
	return s.ConversationEventsBefore(ctx, ident, 0, limit)
}

// ConversationEventsBefore reads one chronological page, using a stable event
// ID cursor. A zero cursor starts at the newest event in this conversation.
func (s *Store) ConversationEventsBefore(ctx context.Context, ident Identity, beforeID int64, limit int) ([]ConversationEvent, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	ident = normalizeIdentity(ident)
	var rows []conversationEventRow
	query := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	err := query.Order("id DESC").Limit(limit).Find(&rows).Error
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
		ID:               row.ID,
		Identity:         Identity{Platform: row.Platform, UserID: row.ExternalUserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID},
		ActorDisplayName: row.ActorDisplayName,
		Source:           row.Source,
		OccurredAt:       row.OccurredAt,
		JobID:            row.JobID,
		DedupeKey:        row.DedupeKey,
		Type:             ConversationEventType(row.Type),
		Content:          row.Content,
		Name:             row.Name,
		ToolCallID:       row.ToolCallID,
		ToolName:         row.ToolName,
		ToolCalls:        calls,
		Parts:            parts,
		CreatedAt:        row.CreatedAt,
	}, nil
}

// ResolveQuotedMessage returns only an accepted Bot outbox message in the
// exact conversation. Pending, rejected, or cross-conversation platform
// messages are deliberately indistinguishable from a missing quote.
func (s *Store) ResolveQuotedMessage(ctx context.Context, conversation message.Conversation, platformMessageID string) (*message.QuotedMessage, error) {
	platform := strings.ToLower(strings.TrimSpace(conversation.Platform))
	conversationType := strings.ToLower(strings.TrimSpace(conversation.Type))
	conversationID := strings.TrimSpace(conversation.ID)
	platformMessageID = strings.TrimSpace(platformMessageID)
	if platform == "" || conversationType == "" || conversationID == "" || platformMessageID == "" {
		return nil, nil
	}
	var row outgoingMessageRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND platform_message_id = ? AND status = ?",
			platform, conversationType, conversationID, platformMessageID, string(delivery.StatusAccepted)).
		Order("id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := outgoingMessageRecord(row)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(record.Message.Content.Text)
	if content == "" && record.Message.Content.Attachment != nil {
		content = strings.TrimSpace(record.Message.Content.Attachment.AltText)
	}
	sentAt := record.CreatedAt.UTC()
	if sentAt.IsZero() {
		sentAt = record.Receipt.AcceptedAt.UTC()
	}
	return &message.QuotedMessage{
		MessageID: platformMessageID,
		Actor:     message.Actor{Platform: platform, UserID: "bot", DisplayName: "Presto"},
		SentAt:    sentAt,
		Content:   content,
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
