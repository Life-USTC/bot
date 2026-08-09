package message

import "time"

// Actor identifies the user that caused an inbound message. It is deliberately
// separate from Conversation because a group conversation has many actors.
type Actor struct {
	Platform string
	UserID   string
}

// Conversation is a platform delivery address.
type Conversation struct {
	Platform string
	Type     string
	ID       string
}

type ReplyRef struct {
	MessageID string
	EventID   string
	Sequence  int
}

type Inbound struct {
	Actor        Actor
	Conversation Conversation
	Source       ReplyRef
	Text         string
	ImageURLs    []string
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
