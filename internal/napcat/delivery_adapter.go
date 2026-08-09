package napcat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

const deliveryPlatform = "napcat"

// DeliveryAdapter translates the channel-neutral delivery model at the
// NapCat boundary. It deliberately bypasses the legacy interaction recorder;
// durable delivery state is owned by delivery.Service and its repository.
type DeliveryAdapter struct {
	bridge *Bridge
}

func NewDeliveryAdapter(bridge *Bridge) *DeliveryAdapter {
	return &DeliveryAdapter{bridge: bridge}
}

func (*DeliveryAdapter) Platform() string { return deliveryPlatform }

func (a *DeliveryAdapter) Deliver(ctx context.Context, outbound message.Outbound) delivery.Outcome {
	if strings.ToLower(strings.TrimSpace(outbound.Target.Platform)) != deliveryPlatform {
		return napcatRejected("invalid_platform", fmt.Errorf("napcat adapter cannot deliver platform %q", outbound.Target.Platform))
	}
	if a == nil || a.bridge == nil {
		return napcatRejected("adapter_unavailable", errors.New("napcat bridge is unavailable"))
	}
	event, err := napcatEventFromConversation(outbound.Target)
	if err != nil {
		return napcatRejected("invalid_target", err)
	}

	payload := any(outbound.Content.Text)
	if attachment := outbound.Content.Attachment; attachment != nil {
		imageURL, err := a.imageURL(attachment)
		if err != nil {
			return napcatRejected("invalid_attachment", err)
		}
		payload = napcatDeliveryMessage(outbound.Content.Text, imageURL)
	}
	conn, writeMu := a.bridge.reverseConnForReply(outbound.ReplyTo)
	var acceptance store.MessageAcceptance
	if conn != nil {
		acceptance, err = a.bridge.sendReversePayload(ctx, conn, writeMu, event, payload)
	} else {
		acceptance, err = a.bridge.sendPayload(ctx, event, payload)
	}
	if err != nil {
		return classifyNapCatDeliveryError(err)
	}
	return delivery.Outcome{State: delivery.OutcomeAccepted, Receipt: napcatReceipt(acceptance)}
}

func napcatEventFromConversation(target message.Conversation) (messageEvent, error) {
	switch strings.ToLower(strings.TrimSpace(target.Type)) {
	case "private":
		id, err := parseNapCatID(target.ID, "user")
		return messageEvent{MessageType: "private", UserID: id}, err
	case "group":
		id, err := parseNapCatID(target.ID, "group")
		return messageEvent{MessageType: "group", GroupID: id}, err
	default:
		return messageEvent{}, fmt.Errorf("napcat does not support conversation type %q", target.Type)
	}
}

func (a *DeliveryAdapter) imageURL(attachment *message.Attachment) (string, error) {
	if imageURL := strings.TrimSpace(attachment.URL); imageURL != "" {
		return imageURL, nil
	}
	if !strings.EqualFold(strings.TrimSpace(attachment.MIMEType), "image/png") {
		return "", fmt.Errorf("napcat byte attachment must be image/png, got %q", attachment.MIMEType)
	}
	if len(attachment.Data) == 0 {
		return "", errors.New("napcat PNG attachment is empty")
	}
	if a.bridge.MediaStore == nil {
		return "", errors.New("napcat media store is unavailable")
	}
	return a.bridge.MediaStore.PutPNG(attachment.Data)
}

func napcatDeliveryMessage(text, imageURL string) []map[string]any {
	segments := make([]map[string]any, 0, 2)
	if text = strings.TrimSpace(text); text != "" {
		segments = append(segments, map[string]any{"type": "text", "data": map[string]any{"text": text}})
	}
	return append(segments, napcatImageMessage(imageURL)...)
}

func classifyNapCatDeliveryError(err error) delivery.Outcome {
	if isUncertainSendError(err) {
		return delivery.Outcome{State: delivery.OutcomeUnknown, Code: "send_uncertain", Err: err}
	}
	var statusErr napcatHTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.status == http.StatusRequestTimeout || statusErr.status == http.StatusTooEarly ||
			statusErr.status == http.StatusTooManyRequests || statusErr.status >= http.StatusInternalServerError {
			return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "platform_unavailable", Err: err}
		}
		return napcatRejected("platform_rejected", err)
	}
	var actionErr napcatActionRejectedError
	if errors.As(err, &actionErr) {
		return napcatRejected("platform_rejected", err)
	}
	if strings.Contains(err.Error(), "not configured") {
		return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "transport_unavailable", Err: err}
	}
	return napcatRejected("delivery_failed", err)
}

func napcatRejected(code string, err error) delivery.Outcome {
	return delivery.Outcome{State: delivery.OutcomeRejected, Code: code, Err: err}
}

func napcatReceipt(acceptance store.MessageAcceptance) message.Receipt {
	return message.Receipt{
		PlatformMessageID: acceptance.PlatformMessageID,
		DeliveryMethod:    acceptance.DeliveryMethod,
		SourceMessageID:   acceptance.SourceMessageID,
		AcceptedAt:        acceptance.AcceptedAt,
	}
}

var _ delivery.Adapter = (*DeliveryAdapter)(nil)
