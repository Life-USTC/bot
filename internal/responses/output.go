package responses

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Life-USTC/Bot/internal/message"
)

// PNGRenderer is the small renderer contract shared by the durable output
// paths. Keeping it here avoids making auth, notifications, or feedback know
// whether production rendering is local or provided by renderd.
type PNGRenderer interface {
	RenderPNGContext(context.Context, *Image) ([]byte, int, int, error)
}

// NewTextCardImage wraps host-authored text in the same generic rich-text card
// used for command results. The complete text is retained in RichText so
// links, codes, and error details remain readable after the text channel is
// removed from delivery.
func NewTextCardImage(kind, text string) *Image {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "message"
	}
	title := textCardTitle(kind)
	return NewRichTextImage(kind, "# "+title+"\n\n"+text, text)
}

func textCardTitle(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "help":
		return "帮助"
	case "schedule", "nextclass":
		return "课表"
	case "calendar":
		return "日程"
	case "todo":
		return "待办"
	case "homework":
		return "作业"
	case "exam":
		return "考试"
	case "account", "login", "login_result", "auth_wait":
		return "登录"
	case "feedback", "feedback_admin":
		return "用户反馈"
	case "notification", "reminder.class", "reminder.homework", "reminder.young":
		return "通知"
	case "agent_confirmation", "command_confirmation", "confirmation":
		return "操作确认"
	case "agent_receipt", "receipt":
		return "操作状态"
	case "agent":
		return "助手"
	case "command", "message":
		return "消息"
	case "error", "failure", "agent_error", "agent_failure", "command_error":
		return "错误"
	default:
		return "Life @ USTC"
	}
}

// RenderTextAttachment renders one host-authored text card into an immutable
// PNG attachment. Callers must inject the renderer used by their deployment.
func RenderTextAttachment(ctx context.Context, renderer PNGRenderer, kind, text, ref string) (*message.Attachment, error) {
	image := NewTextCardImage(kind, text)
	if image == nil {
		return nil, errors.New("host text is empty")
	}
	image.Ref = ref
	return RenderImageAttachment(ctx, renderer, image)
}

// EncodeImageIntent serializes a structured card for durable delivery. The
// payload contains the renderer input rather than rendered bytes, so a
// render failure can retry without rerunning the command or its side effects.
func EncodeImageIntent(image *Image) ([]byte, error) {
	if image == nil {
		return nil, errors.New("response image is empty")
	}
	return json.Marshal(image)
}

// DecodeImageIntent restores a structured card persisted in an outbound
// attachment before the delivery service renders it.
func DecodeImageIntent(payload []byte) (*Image, error) {
	if len(payload) == 0 {
		return nil, errors.New("response image render payload is empty")
	}
	var image Image
	if err := json.Unmarshal(payload, &image); err != nil {
		return nil, err
	}
	return &image, nil
}

// RenderImageAttachment renders an already structured host card. It is kept
// separate from RenderTextAttachment so specialized cards retain their layout
// while sharing the same required renderer and empty-PNG checks.
func RenderImageAttachment(ctx context.Context, renderer PNGRenderer, image *Image) (*message.Attachment, error) {
	if image == nil {
		return nil, errors.New("response image is empty")
	}
	if renderer == nil {
		return nil, errors.New("response renderer is unavailable")
	}
	data, _, _, err := renderer.RenderPNGContext(ctx, image)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("response renderer returned an empty PNG")
	}
	return &message.Attachment{MIMEType: "image/png", Data: data, AltText: image.AltText}, nil
}
