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
