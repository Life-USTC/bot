package qqbot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	item := qqDeliveryMessage{Text: outbound.Content.ExplicitTextContent()}
	for _, part := range outbound.Content.Parts {
		if part.Attachment == nil {
			continue
		}
		if item.Attachment != nil {
			return qqRejected("invalid_message", errors.New("QQ requires each image to have its own durable Outbox record"))
		}
		item.Attachment = part.Attachment
	}
	msgID, eventID, sequence := qqReplyReference(outbound.ReplyTo)
	acceptance, err := a.deliverMessage(ctx, ident, item, msgID, eventID, sequence)
	if err != nil {
		return classifyQQDeliveryError(err)
	}
	return delivery.Outcome{State: delivery.OutcomeAccepted, Receipt: qqReceipt(acceptance)}
}

type qqDeliveryMessage struct {
	Text       string
	Attachment *message.Attachment
}

func (a *DeliveryAdapter) deliverMessage(
	ctx context.Context,
	ident store.Identity,
	item qqDeliveryMessage,
	msgID, eventID string,
	sequence int,
) (store.MessageAcceptance, error) {
	if item.Attachment == nil {
		return a.bot.sendTo(ctx, ident, qqBotOutgoingMessage(ident, item.Text), msgID, eventID, sequence)
	}
	if _, mediaErr := richMediaUploadPath(ident); mediaErr != nil {
		return store.MessageAcceptance{}, invalidAttachmentError{err: mediaErr}
	}
	imageData, imageErr := a.imageBytes(ctx, item.Attachment)
	if imageErr != nil {
		if strings.TrimSpace(item.Attachment.URL) != "" {
			return store.MessageAcceptance{}, attachmentUnavailableError{err: imageErr}
		}
		return store.MessageAcceptance{}, invalidAttachmentError{err: imageErr}
	}
	return a.bot.sendCachedRichMediaContent(
		ctx, ident, imageData, qqBotOutgoingMessage(ident, item.Text), msgID, eventID, sequence,
	)
}

type attachmentUnavailableError struct{ err error }

func (e attachmentUnavailableError) Error() string { return e.err.Error() }
func (e attachmentUnavailableError) Unwrap() error { return e.err }

type invalidAttachmentError struct{ err error }

func (e invalidAttachmentError) Error() string { return e.err.Error() }
func (e invalidAttachmentError) Unwrap() error { return e.err }

func qqIdentityFromConversation(target message.Conversation) (store.Identity, error) {
	conversationType := strings.ToLower(strings.TrimSpace(target.Type))
	switch conversationType {
	case "private", "group", "channel", "guild_private":
	default:
		return store.Identity{}, fmt.Errorf("qqbot does not support conversation type %q", target.Type)
	}
	id := strings.TrimSpace(target.ID)
	if id == "" {
		return store.Identity{}, errors.New("qqbot conversation ID is empty")
	}
	ident := store.Identity{Platform: deliveryPlatform, ConversationType: conversationType, ConversationID: id}
	if conversationType == "private" || conversationType == "guild_private" {
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

func (a *DeliveryAdapter) imageBytes(ctx context.Context, attachment *message.Attachment) ([]byte, error) {
	if imageURL := strings.TrimSpace(attachment.URL); imageURL != "" {
		return a.downloadPNG(ctx, imageURL)
	}
	if !strings.EqualFold(strings.TrimSpace(attachment.MIMEType), "image/png") {
		return nil, fmt.Errorf("qqbot byte attachment must be image/png, got %q", attachment.MIMEType)
	}
	if len(attachment.Data) == 0 {
		return nil, errors.New("qqbot PNG attachment is empty")
	}
	if err := validatePNG(attachment.Data); err != nil {
		return nil, err
	}
	return append([]byte(nil), attachment.Data...), nil
}

func (a *DeliveryAdapter) downloadPNG(ctx context.Context, imageURL string) ([]byte, error) {
	parsed, err := url.Parse(imageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("qqbot image URL must use http or https")
	}
	downloadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, errors.New("qqbot image URL request is invalid")
	}
	resp, err := a.bot.httpClient().Do(req)
	if err != nil {
		if downloadCtx.Err() != nil {
			return nil, downloadCtx.Err()
		}
		return nil, errors.New("qqbot image URL download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qqbot image URL returned HTTP %d", resp.StatusCode)
	}
	const maxDownloadBytes = 10 << 20
	if resp.ContentLength > maxDownloadBytes {
		return nil, errors.New("qqbot image URL exceeds 10 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, errors.New("qqbot image URL download failed")
	}
	if len(data) > maxDownloadBytes {
		return nil, errors.New("qqbot image URL exceeds 10 MiB")
	}
	if err := validatePNG(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validatePNG(data []byte) error {
	if len(data) == 0 {
		return errors.New("qqbot PNG attachment is empty")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 {
		return errors.New("qqbot attachment is not a valid PNG")
	}
	return nil
}

func classifyQQDeliveryError(err error) delivery.Outcome {
	if isUncertainSendError(err) {
		return delivery.Outcome{State: delivery.OutcomeUnknown, Code: "send_uncertain", Err: err}
	}
	var attachmentErr attachmentUnavailableError
	if errors.As(err, &attachmentErr) {
		return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "attachment_unavailable", Err: err}
	}
	var invalidAttachment invalidAttachmentError
	if errors.As(err, &invalidAttachment) {
		return qqRejected("invalid_attachment", err)
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
	if errors.As(cause, &statusErr) {
		if statusErr.code == 40093001 || !isPermanentHTTPStatus(statusErr.status) {
			return delivery.Outcome{State: delivery.OutcomeRetryable, Code: "pre_send_failed", Err: original}
		}
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
