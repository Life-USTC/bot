package message

import (
	"strings"
	"time"
)

// Actor identifies the user that caused an inbound message. It is deliberately
// separate from Conversation because a group conversation has many actors.
type Actor struct {
	Platform    string
	UserID      string
	DisplayName string
}

// Conversation is a platform delivery address.
type Conversation struct {
	Platform string
	Type     string
	ID       string
}

type ReplyRef struct {
	MessageID   string
	EventID     string
	Sequence    int
	TransportID string
}

// QuotedMessage is the immutable context of a message being replied to. It
// is populated only after the application has verified that the referenced
// message is this bot's accepted outbox message in the same conversation.
type QuotedMessage struct {
	MessageID string
	Actor     Actor
	SentAt    time.Time
	Content   string
}

// InputMediaKind identifies a piece of media attached to an inbound message.
// The value describes the platform-provided media, not whether the model can
// understand it. Unsupported kinds are retained so the model can be told that
// an attachment was present instead of silently dropping user input.
type InputMediaKind string

const (
	InputMediaImage   InputMediaKind = "image"
	InputMediaSticker InputMediaKind = "sticker"
	InputMediaFile    InputMediaKind = "file"
	InputMediaAudio   InputMediaKind = "audio"
	InputMediaVideo   InputMediaKind = "video"
	InputMediaUnknown InputMediaKind = "unknown"
)

// InputMedia is a platform-neutral reference to user-supplied inbound media.
// Bytes are intentionally not carried in the inbound envelope: platform
// adapters retain references and the model layer fetches or parses them under
// its own bounded input policy.
type InputMedia struct {
	Kind     InputMediaKind `json:"kind"`
	URL      string         `json:"url,omitempty"`
	MIMEType string         `json:"mime_type,omitempty"`
	Name     string         `json:"name,omitempty"`
	FileID   string         `json:"file_id,omitempty"`
	Size     int64          `json:"size,omitempty"`
}

// InputPart preserves the order of text, media and nested forwarded content.
// Forward points to a nested message so a forwarded tree does not have to be
// flattened before the agent decides how to present it to the model.
type InputPart struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	Media     *InputMedia       `json:"media,omitempty"`
	Forward   *ForwardedMessage `json:"forward,omitempty"`
	ForwardID string            `json:"forward_id,omitempty"`
}

// ForwardedMessage retains the original speaker and source time for one node
// in a merge-forward message. Parts may contain another ForwardedMessage.
type ForwardedMessage struct {
	Speaker Actor       `json:"speaker"`
	SentAt  time.Time   `json:"sent_at,omitempty"`
	Text    string      `json:"text,omitempty"`
	Parts   []InputPart `json:"parts,omitempty"`
}

type Inbound struct {
	Actor        Actor
	Conversation Conversation
	Source       ReplyRef
	ReplyTo      *ReplyRef
	ReplyContext *QuotedMessage
	SentAt       time.Time
	ReceivedAt   time.Time
	Text         string
	ImageURLs    []string
	// Parts is the ordered top-level input. Media and Forwarded are retained as
	// convenient indexes for callers that do not need to render the whole tree.
	Parts        []InputPart
	Media        []InputMedia
	Forwarded    []ForwardedMessage
	BotMentioned bool
}

// ResponseContext marks a verified Bot message. Public command responses also
// carry the structured invocation so a reply can apply a small deterministic
// follow-up without replaying the surrounding group conversation through an
// LLM.
type ResponseContext struct {
	Capability string   `json:"capability"`
	Arguments  []string `json:"arguments,omitempty"`
}

// Attachment contains immutable, delivery-ready media. Rendering belongs to
// the presentation layer; platform adapters only upload or reference it.
type Attachment struct {
	MIMEType string
	Data     []byte
	URL      string
	AltText  string
}

// ContentPart is one ordered piece of an outbound message. Text and an
// attachment may be present together when the platform supports a caption;
// adapters otherwise preserve the order by splitting the part as needed.
type ContentPart struct {
	Text       string
	Attachment *Attachment
}

type Content struct {
	Parts []ContentPart
}

// HasContent reports whether at least one non-empty text or attachment part
// can be delivered.
func (c Content) HasContent() bool {
	for _, part := range c.Parts {
		if strings.TrimSpace(part.Text) != "" || part.Attachment != nil {
			return true
		}
	}
	return false
}

// TextContent returns the text carried by the content in display order. When
// a message contains only images, their alt text is retained as quote
// context. It deliberately does not expose attachment bytes or URLs as
// user-authored text.
func (c Content) TextContent() string {
	texts := make([]string, 0, len(c.Parts))
	alts := make([]string, 0, len(c.Parts))
	for _, part := range c.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			texts = append(texts, text)
		}
		if part.Attachment != nil {
			if alt := strings.TrimSpace(part.Attachment.AltText); alt != "" {
				alts = append(alts, alt)
			}
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n\n")
	}
	return strings.Join(alts, "\n\n")
}

// SingleImageMessages groups text with each image for APIs that accept one
// image per message. Each group must be persisted as a separate Outbox record
// so partial acceptance never causes a previously sent image to be replayed.
func (c Content) SingleImageMessages() []Content {
	var groups []Content
	current := Content{}
	hasImage := false
	for _, part := range c.Parts {
		if part.Attachment != nil && hasImage {
			groups = append(groups, current)
			current = Content{}
			hasImage = false
		}
		current.Parts = append(current.Parts, part)
		hasImage = hasImage || part.Attachment != nil
	}
	if current.HasContent() {
		groups = append(groups, current)
	}
	return groups
}

type Outbound struct {
	Kind      string
	Target    Conversation
	ReplyTo   *ReplyRef
	Context   *ResponseContext
	Content   Content
	DedupeKey string
	ExpiresAt time.Time
}

type Receipt struct {
	PlatformMessageID string
	DeliveryMethod    string
	SourceMessageID   string
	AcceptedAt        time.Time
}
