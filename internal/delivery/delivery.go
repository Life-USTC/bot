package delivery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusDelivering Status = "delivering"
	StatusAccepted   Status = "accepted"
	StatusRetryWait  Status = "retry_wait"
	StatusRejected   Status = "rejected"
	StatusUnknown    Status = "unknown"
	StatusExpired    Status = "expired"
)

type OutcomeState string

const (
	OutcomeAccepted  OutcomeState = "accepted"
	OutcomeRetryable OutcomeState = "retryable"
	OutcomeRejected  OutcomeState = "rejected"
	OutcomeUnknown   OutcomeState = "unknown"
)

type Outcome struct {
	State   OutcomeState
	Receipt message.Receipt
	Code    string
	Err     error
}

type Adapter interface {
	Platform() string
	Deliver(context.Context, message.Outbound) Outcome
}

type Record struct {
	ID               int64
	Message          message.Outbound
	Status           Status
	Attempts         int
	NextAttemptAt    time.Time
	AttemptStartedAt time.Time
	Receipt          message.Receipt
	ErrorCode        string
	ErrorMessage     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Repository interface {
	Enqueue(context.Context, message.Outbound) (Record, bool, error)
	ClaimDue(context.Context, time.Time, int) ([]Record, error)
	Complete(context.Context, int64, Outcome, time.Time) error
	ExpireDue(context.Context, time.Time) error
	RecoverStale(context.Context, time.Time) error
	PruneOutgoingMessages(context.Context, time.Time) error
}

type Service struct {
	repository Repository
	adapters   map[string]Adapter
	renderer   responses.PNGRenderer
}

func New(repository Repository, adapters ...Adapter) (*Service, error) {
	service := &Service{repository: repository, adapters: make(map[string]Adapter, len(adapters))}
	for _, adapter := range adapters {
		if err := service.Register(adapter); err != nil {
			return nil, err
		}
	}
	return service, nil
}

// Register adds a platform adapter while the application is being composed.
// All adapters must be registered before a Worker starts using the service.
func (s *Service) Register(adapter Adapter) error {
	if adapter == nil {
		return nil
	}
	platform := normalizePlatform(adapter.Platform())
	if platform == "" {
		return errors.New("delivery adapter platform is empty")
	}
	if _, exists := s.adapters[platform]; exists {
		return fmt.Errorf("duplicate delivery adapter for platform %q", platform)
	}
	s.adapters[platform] = adapter
	return nil
}

// SetRenderer injects the renderer used at the durable delivery boundary.
// Renderers are intentionally absent from producer transactions: an outbox
// record keeps the image intent and this service renders it for each attempt.
func (s *Service) SetRenderer(renderer responses.PNGRenderer) error {
	if s == nil {
		return errors.New("delivery service is unavailable")
	}
	if renderer == nil {
		return errors.New("delivery renderer is unavailable")
	}
	s.renderer = renderer
	return nil
}

func (s *Service) Enqueue(ctx context.Context, outbound message.Outbound) (Record, bool, error) {
	if s == nil || s.repository == nil {
		return Record{}, false, errors.New("delivery repository is unavailable")
	}
	if err := validateOutbound(outbound, true); err != nil {
		return Record{}, false, err
	}
	return s.repository.Enqueue(ctx, normalizeOutbound(outbound))
}

// DeliverRecord delivers a claimed outbox record. The record ID is the
// request number stamped on every card the attempt renders, so the identifier
// a user reads in a footer names the durable row an operator can look up. A
// retry of the same record renders the same number.
func (s *Service) DeliverRecord(ctx context.Context, record Record) Outcome {
	return s.deliver(ctx, record.Message, formatRef(record.ID))
}

func (s *Service) DeliverNow(ctx context.Context, outbound message.Outbound) Outcome {
	return s.deliver(ctx, outbound, "")
}

func formatRef(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (s *Service) deliver(ctx context.Context, outbound message.Outbound, ref string) Outcome {
	if s == nil {
		return Outcome{State: OutcomeRejected, Code: "delivery_unavailable", Err: errors.New("delivery service is unavailable")}
	}
	if err := validateOutbound(outbound, false); err != nil {
		return Outcome{State: OutcomeRejected, Code: "invalid_message", Err: err}
	}
	outbound = normalizeOutbound(outbound)
	if !outbound.ExpiresAt.IsZero() && !outbound.ExpiresAt.After(time.Now()) {
		return Outcome{State: OutcomeRejected, Code: "expired", Err: errors.New("message expired before delivery")}
	}
	rendered, err := s.renderOutbound(ctx, outbound, ref)
	if err != nil {
		return Outcome{State: OutcomeRetryable, Code: "render_failed", Err: err}
	}
	outbound = rendered
	// Rendering can outlast a reminder's deadline even after a valid claim.
	if !outbound.ExpiresAt.IsZero() && !outbound.ExpiresAt.After(time.Now()) {
		return Outcome{State: OutcomeRejected, Code: "expired", Err: errors.New("message expired while rendering")}
	}
	adapter := s.adapters[outbound.Target.Platform]
	if adapter == nil {
		return Outcome{
			State: OutcomeRejected,
			Code:  "unsupported_platform",
			Err:   fmt.Errorf("no delivery adapter configured for platform %q", outbound.Target.Platform),
		}
	}
	return normalizeOutcome(adapter.Deliver(ctx, outbound))
}

// renderOutbound turns host-authored text and structured render intents into
// delivery-ready attachments. It runs after an outbox record has been
// claimed, so a renderer failure retries only output and never reruns the
// business operation that created the record.
func (s *Service) renderOutbound(ctx context.Context, outbound message.Outbound, ref string) (message.Outbound, error) {
	if outbound.TextPolicy == message.TextPolicyLLM {
		return renderStructuredAttachments(ctx, s.renderer, outbound, false, ref)
	}
	if outbound.TextPolicy != message.TextPolicyImageOnly {
		return message.Outbound{}, fmt.Errorf("unsupported text policy %q", outbound.TextPolicy)
	}
	if s.renderer == nil {
		for _, part := range outbound.Content.Parts {
			if (strings.TrimSpace(part.Text) != "" && part.Attachment == nil) ||
				(part.Attachment != nil && len(part.Attachment.RenderPayload) > 0) {
				return message.Outbound{}, errors.New("delivery renderer is unavailable")
			}
		}
	}
	rendered, err := renderStructuredAttachments(ctx, s.renderer, outbound, true, ref)
	if err != nil {
		return message.Outbound{}, err
	}
	for _, part := range rendered.Content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			return message.Outbound{}, errors.New("image-only delivery retained text")
		}
		if part.Attachment == nil {
			return message.Outbound{}, errors.New("image-only delivery retained an empty part")
		}
	}
	return rendered, nil
}

func renderStructuredAttachments(ctx context.Context, renderer responses.PNGRenderer, outbound message.Outbound, renderText bool, ref string) (message.Outbound, error) {
	parts := make([]message.ContentPart, 0, len(outbound.Content.Parts))
	for _, part := range outbound.Content.Parts {
		if strings.TrimSpace(part.Text) != "" && renderText && part.Attachment == nil {
			attachment, err := responses.RenderTextAttachment(ctx, renderer, outbound.Kind, part.Text, ref)
			if err != nil {
				return message.Outbound{}, err
			}
			attachment.AltText = strings.TrimSpace(part.Text)
			parts = append(parts, message.ContentPart{Attachment: attachment})
		} else if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, message.ContentPart{Text: strings.TrimSpace(part.Text)})
		}
		if part.Attachment == nil {
			continue
		}
		attachment := *part.Attachment
		if len(attachment.RenderPayload) > 0 {
			image, err := responses.DecodeImageIntent(attachment.RenderPayload)
			if err != nil {
				return message.Outbound{}, fmt.Errorf("decode image render intent: %w", err)
			}
			image.Ref = ref
			if url := strings.TrimSpace(image.URL); url != "" {
				attachment = message.Attachment{MIMEType: "image/png", URL: url, AltText: image.AltText}
			} else {
				rendered, err := responses.RenderImageAttachment(ctx, renderer, image)
				if err != nil {
					return message.Outbound{}, err
				}
				rendered.AltText = image.AltText
				attachment = *rendered
			}
		}
		attachment.RenderPayload = nil
		parts = append(parts, message.ContentPart{Attachment: &attachment})
	}
	outbound.Content.Parts = parts
	return outbound, nil
}

func validateOutbound(outbound message.Outbound, durable bool) error {
	if normalizePlatform(outbound.Target.Platform) == "" {
		return errors.New("delivery target platform is empty")
	}
	if strings.TrimSpace(outbound.Target.Type) == "" || strings.TrimSpace(outbound.Target.ID) == "" {
		return errors.New("delivery target conversation is incomplete")
	}
	if !outbound.Content.HasContent() {
		return errors.New("delivery content is empty")
	}
	switch outbound.TextPolicy {
	case message.TextPolicyImageOnly, message.TextPolicyLLM:
		// Host text is a durable image intent and is rendered by DeliverNow.
	default:
		return fmt.Errorf("unsupported text policy %q", outbound.TextPolicy)
	}
	if durable && strings.TrimSpace(outbound.DedupeKey) == "" {
		return errors.New("durable delivery dedupe key is empty")
	}
	return nil
}

func normalizeOutbound(outbound message.Outbound) message.Outbound {
	outbound.Kind = strings.ToLower(strings.TrimSpace(outbound.Kind))
	outbound.Target.Platform = normalizePlatform(outbound.Target.Platform)
	outbound.Target.Type = strings.ToLower(strings.TrimSpace(outbound.Target.Type))
	outbound.Target.ID = strings.TrimSpace(outbound.Target.ID)
	parts := make([]message.ContentPart, 0, len(outbound.Content.Parts))
	for _, part := range outbound.Content.Parts {
		part.Text = strings.TrimSpace(part.Text)
		if part.Attachment != nil {
			attachment := *part.Attachment
			attachment.MIMEType = strings.TrimSpace(attachment.MIMEType)
			attachment.URL = strings.TrimSpace(attachment.URL)
			attachment.AltText = strings.TrimSpace(attachment.AltText)
			attachment.Data = append([]byte(nil), attachment.Data...)
			attachment.RenderPayload = append([]byte(nil), attachment.RenderPayload...)
			part.Attachment = &attachment
		}
		if part.Text == "" && part.Attachment == nil {
			continue
		}
		parts = append(parts, part)
	}
	outbound.Content.Parts = parts
	outbound.DedupeKey = strings.TrimSpace(outbound.DedupeKey)
	return outbound
}

func normalizeOutcome(outcome Outcome) Outcome {
	switch outcome.State {
	case OutcomeAccepted, OutcomeRetryable, OutcomeRejected, OutcomeUnknown:
		return outcome
	default:
		outcome.State = OutcomeUnknown
		if outcome.Code == "" {
			outcome.Code = "invalid_adapter_outcome"
		}
		if outcome.Err == nil {
			outcome.Err = errors.New("delivery adapter returned an invalid outcome")
		}
		return outcome
	}
}

func normalizePlatform(platform string) string {
	return strings.ToLower(strings.TrimSpace(platform))
}
