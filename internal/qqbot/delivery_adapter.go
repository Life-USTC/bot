package qqbot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

const deliveryPlatform = "qqbot"

// DeliveryAdapter owns the conversion from neutral conversations to QQ OpenID
// targets. It does not record interactions; delivery.Service owns persistence.
type DeliveryAdapter struct {
	bot *Bot
}

func NewDeliveryAdapter(bot *Bot) *DeliveryAdapter {
	return &DeliveryAdapter{bot: bot}
}

func (*DeliveryAdapter) Platform() string { return deliveryPlatform }

func (a *DeliveryAdapter) Deliver(ctx context.Context, outbound message.Outbound) delivery.Outcome {
	if strings.ToLower(strings.TrimSpace(outbound.Target.Platform)) != deliveryPlatform {
		return qqRejected("invalid_platform", fmt.Errorf("qqbot adapter cannot deliver platform %q", outbound.Target.Platform))
	}
	if a == nil || a.bot == nil {
		return qqRejected("adapter_unavailable", errors.New("qq bot is unavailable"))
	}
	ident, err := qqIdentityFromConversation(outbound.Target)
	if err != nil {
		return qqRejected("invalid_target", err)
	}
	msgID, eventID, sequence := qqReplyReference(outbound.ReplyTo)

	var acceptance store.MessageAcceptance
	if attachment := outbound.Content.Attachment; attachment != nil {
		imageURL, imageErr := a.imageURL(attachment)
		if imageErr != nil {
			return qqRejected("invalid_attachment", imageErr)
		}
		acceptance, err = a.bot.sendCachedRichMediaContent(
			ctx, ident, imageURL, qqBotOutgoingMessage(ident, outbound.Content.Text), msgID, eventID, sequence,
		)
	} else {
		text := qqBotOutgoingMessage(ident, outbound.Content.Text)
		acceptance, err = a.bot.sendTo(ctx, ident, text, msgID, eventID, sequence)
	}
	if err != nil {
		return classifyQQDeliveryError(err)
	}
	return delivery.Outcome{State: delivery.OutcomeAccepted, Receipt: qqReceipt(acceptance)}
}

func qqIdentityFromConversation(target message.Conversation) (store.Identity, error) {
	conversationType := strings.ToLower(strings.TrimSpace(target.Type))
	if conversationType != "private" && conversationType != "group" {
		return store.Identity{}, fmt.Errorf("qqbot does not support conversation type %q", target.Type)
	}
	id := strings.TrimSpace(target.ID)
	if id == "" {
		return store.Identity{}, errors.New("qqbot conversation ID is empty")
	}
	ident := store.Identity{Platform: deliveryPlatform, ConversationType: conversationType, ConversationID: id}
	if conversationType == "private" {
		ident.UserID = id
	}
	return ident, nil
}

func qqReplyReference(ref *message.ReplyRef) (string, string, int) {
	if ref == nil {
		return "", "", 0
	}
	sequence := ref.Sequence
	if sequence <= 0 && (strings.TrimSpace(ref.MessageID) != "" || strings.TrimSpace(ref.EventID) != "") {
		sequence = 1
	}
	return strings.TrimSpace(ref.MessageID), strings.TrimSpace(ref.EventID), sequence
}

func (a *DeliveryAdapter) imageURL(attachment *message.Attachment) (string, error) {
	if imageURL := strings.TrimSpace(attachment.URL); imageURL != "" {
		return imageURL, nil
	}
	if !strings.EqualFold(strings.TrimSpace(attachment.MIMEType), "image/png") {
		return "", fmt.Errorf("qqbot byte attachment must be image/png, got %q", attachment.MIMEType)
	}
	if len(attachment.Data) == 0 {
		return "", errors.New("qqbot PNG attachment is empty")
	}
	if a.bot.MediaStore == nil {
		return "", errors.New("qqbot media store is unavailable")
	}
	return a.bot.MediaStore.PutPNG(attachment.Data)
}

func classifyQQDeliveryError(err error) delivery.Outcome {
	if isUncertainSendError(err) {
		return delivery.Outcome{State: delivery.OutcomeUnknown, Code: "send_uncertain", Err: err}
	}
	var beforeSend preSendError
	if errors.As(err, &beforeSend) {
		return classifyQQPreSendError(err, beforeSend.err)
	}
	var statusErr qqBotHTTPStatusError
	if errors.As(err, &statusErr) {
		if !isPermanentHTTPStatus(statusErr.status) {
			return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "platform_unavailable", Err: err}
		}
		return qqRejected("platform_rejected", err)
	}
	var permanent *permanentGatewayError
	if errors.As(err, &permanent) {
		return qqRejected("credentials_rejected", err)
	}
	return qqRejected("delivery_failed", err)
}

func classifyQQPreSendError(original, cause error) delivery.Outcome {
	var statusErr qqBotHTTPStatusError
	if errors.As(cause, &statusErr) && isPermanentHTTPStatus(statusErr.status) {
		return qqRejected("platform_rejected", original)
	}
	var permanent *permanentGatewayError
	if errors.As(cause, &permanent) {
		return qqRejected("credentials_rejected", original)
	}
	return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "pre_send_failed", Err: original}
}

func qqRejected(code string, err error) delivery.Outcome {
	return delivery.Outcome{State: delivery.OutcomeRejected, Code: code, Err: err}
}

func qqReceipt(acceptance store.MessageAcceptance) message.Receipt {
	return message.Receipt{
		PlatformMessageID: acceptance.PlatformMessageID,
		DeliveryMethod:    acceptance.DeliveryMethod,
		SourceMessageID:   acceptance.SourceMessageID,
		AcceptedAt:        acceptance.AcceptedAt,
	}
}

var _ delivery.Adapter = (*DeliveryAdapter)(nil)
