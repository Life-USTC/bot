package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/message"
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
	ReadyToDeliver(context.Context, int64) (bool, error)
	Complete(context.Context, int64, Outcome, time.Time) error
	ExpireDue(context.Context, time.Time) error
	RecoverStale(context.Context, time.Time) error
}

type Service struct {
	repository Repository
	adapters   map[string]Adapter
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

func (s *Service) Enqueue(ctx context.Context, outbound message.Outbound) (Record, bool, error) {
	if s == nil || s.repository == nil {
		return Record{}, false, errors.New("delivery repository is unavailable")
	}
	if err := validateOutbound(outbound, true); err != nil {
		return Record{}, false, err
	}
	return s.repository.Enqueue(ctx, normalizeOutbound(outbound))
}

func (s *Service) DeliverNow(ctx context.Context, outbound message.Outbound) Outcome {
	if err := validateOutbound(outbound, false); err != nil {
		return Outcome{State: OutcomeRejected, Code: "invalid_message", Err: err}
	}
	outbound = normalizeOutbound(outbound)
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

func validateOutbound(outbound message.Outbound, durable bool) error {
	if normalizePlatform(outbound.Target.Platform) == "" {
		return errors.New("delivery target platform is empty")
	}
	if strings.TrimSpace(outbound.Target.Type) == "" || strings.TrimSpace(outbound.Target.ID) == "" {
		return errors.New("delivery target conversation is incomplete")
	}
	if strings.TrimSpace(outbound.Content.Text) == "" && outbound.Content.Attachment == nil {
		return errors.New("delivery content is empty")
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
	outbound.Content.Text = strings.TrimSpace(outbound.Content.Text)
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
