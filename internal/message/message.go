package message

import "time"

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

type Content struct {
	Text       string
	Attachment *Attachment
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
