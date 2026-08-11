package botapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
)

const defaultImageRenderTimeout = 5 * time.Second

type renderedImage struct {
	data []byte
	err  error
}

func normalizedImageRenderTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultImageRenderTimeout
	}
	return timeout
}

func (a *App) deliverResponsePart(ctx context.Context, inbound message.Inbound, response commands.Response, attempt int) (delivery.Outcome, int) {
	if response.Image == nil {
		return a.deliverContent(ctx, inbound, response.Kind, message.Content{Text: response.Text}, attempt), 1
	}

	attachment, err := a.renderAttachment(ctx, response.Image)
	if err != nil {
		a.logf("render immediate reply failed: %v", err)
		return a.deliverFallbackText(ctx, inbound, response, attempt), 1
	}
	imageOutcome := a.deliverContent(ctx, inbound, response.Kind, message.Content{Attachment: attachment}, attempt)
	if imageOutcome.State == delivery.OutcomeAccepted || imageOutcome.State == delivery.OutcomeUnknown {
		return imageOutcome, 1
	}
	if strings.TrimSpace(response.Text) == "" && strings.TrimSpace(response.Image.AltText) == "" {
		return imageOutcome, 1
	}
	a.logf("image reply failed; falling back to text: platform=%s conversation_type=%s conversation_id=%s state=%s code=%s error=%v",
		inbound.Conversation.Platform, inbound.Conversation.Type, inbound.Conversation.ID,
		imageOutcome.State, imageOutcome.Code, imageOutcome.Err)
	return a.deliverFallbackText(ctx, inbound, response, attempt+1), 2
}

func (a *App) deliverFallbackText(ctx context.Context, inbound message.Inbound, response commands.Response, attempt int) delivery.Outcome {
	text := strings.TrimSpace(response.Text)
	if text == "" && response.Image != nil {
		text = strings.TrimSpace(response.Image.AltText)
	}
	return a.deliverContent(ctx, inbound, response.Kind, message.Content{Text: text}, attempt)
}

func (a *App) deliverContent(ctx context.Context, inbound message.Inbound, kind string, content message.Content, attempt int) delivery.Outcome {
	reply := inbound.Source
	baseSequence := reply.Sequence
	if baseSequence <= 0 {
		baseSequence = 1
	}
	reply.Sequence = baseSequence + attempt
	outcome := a.delivery.DeliverNow(ctx, message.Outbound{
		Kind: kind, Target: inbound.Conversation, ReplyTo: &reply, Content: content,
	})
	a.recordOutbound(ctx, inbound, content.Text, outcome)
	return outcome
}

func (a *App) renderAttachment(ctx context.Context, image *responses.Image) (*message.Attachment, error) {
	if image == nil {
		return nil, errors.New("response image is nil")
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		return &message.Attachment{MIMEType: "image/png", URL: url}, nil
	}
	if a.renderer == nil {
		return nil, errors.New("response renderer is unavailable")
	}
	renderCtx, cancel := context.WithTimeout(ctx, a.imageRenderTimeout)
	defer cancel()
	result := make(chan renderedImage, 1)
	go func() {
		data, _, _, err := a.renderer.RenderPNG(image)
		result <- renderedImage{data: data, err: err}
	}()
	select {
	case <-renderCtx.Done():
		return nil, renderCtx.Err()
	case rendered := <-result:
		if rendered.err != nil {
			return nil, rendered.err
		}
		return &message.Attachment{MIMEType: "image/png", Data: rendered.data}, nil
	}
}
