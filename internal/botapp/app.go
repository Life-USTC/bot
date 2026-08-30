package botapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type CommandHandler interface {
	HandleResponse(context.Context, commands.Input) (commands.Response, bool)
}

type AgentHandler interface {
	HandleResponse(context.Context, agent.Input) (commands.Response, bool)
}

type Dispatcher interface {
	Submit(agent.Input, agent.DispatchCallback)
}

type Deliverer interface {
	DeliverNow(context.Context, message.Outbound) delivery.Outcome
}

type Recorder interface {
	RecordInteraction(context.Context, store.Identity, store.Interaction) error
}

type Renderer interface {
	RenderPNG(*responses.Image) ([]byte, int, int, error)
}

type Processor interface {
	Process(context.Context, message.Inbound)
}

type Config struct {
	Commands           CommandHandler
	Agent              AgentHandler
	Dispatcher         Dispatcher
	Delivery           Deliverer
	Recorder           Recorder
	Renderer           Renderer
	ImageRenderTimeout time.Duration
	Logger             *log.Logger
}

// App is the sole application layer for inbound bot messages. Protocol
// adapters translate events into message.Inbound and hand them to Process.
type App struct {
	commands           CommandHandler
	agent              AgentHandler
	dispatcher         Dispatcher
	delivery           Deliverer
	recorder           Recorder
	renderer           Renderer
	imageRenderTimeout time.Duration
	logger             *log.Logger
}

func New(config Config) (*App, error) {
	if config.Commands == nil {
		return nil, errors.New("botapp command handler is unavailable")
	}
	if config.Delivery == nil {
		return nil, errors.New("botapp delivery service is unavailable")
	}
	return &App{
		commands: config.Commands, agent: config.Agent, dispatcher: config.Dispatcher,
		delivery: config.Delivery, recorder: config.Recorder, renderer: config.Renderer,
		imageRenderTimeout: normalizedImageRenderTimeout(config.ImageRenderTimeout), logger: config.Logger,
	}, nil
}

// Process accepts one inbound message. Command replies are immediate; free-form
// messages use the dispatcher when configured so follow-ups keep their existing
// debounce and conversation semantics.
func (a *App) Process(ctx context.Context, inbound message.Inbound) {
	if err := validateInbound(inbound); err != nil {
		a.logf("reject inbound message: %v", err)
		return
	}
	if reply, ok := a.commands.HandleResponse(ctx, commands.Input{
		Text: inbound.Text, Identity: legacyIdentity(inbound), BotMentioned: inbound.BotMentioned,
	}); ok {
		a.deliverResponse(ctx, inbound, reply)
		return
	}
	if a.dispatcher != nil {
		a.dispatcher.Submit(a.agentInput(inbound), func(ctx context.Context, input agent.Input, reply commands.Response, ok bool) {
			merged := inbound
			merged.Text = input.Text
			merged.ImageURLs = append([]string(nil), input.ImageURLs...)
			a.finishAgent(ctx, merged, reply, ok)
		})
		return
	}
	if a.agent == nil {
		a.recordIgnored(ctx, inbound)
		return
	}
	reply, ok := a.agent.HandleResponse(ctx, a.agentInput(inbound))
	a.finishAgent(ctx, inbound, reply, ok)
}

func (a *App) finishAgent(ctx context.Context, inbound message.Inbound, reply commands.Response, ok bool) {
	if !ok {
		a.recordIgnored(ctx, inbound)
		return
	}
	a.record(ctx, inbound, store.Interaction{
		RawText: inbound.Text, Command: "agent", Handled: true, Reply: reply.Text,
		Status: store.InteractionStatusHandled,
	}, "agent")
	a.deliverResponse(ctx, inbound, reply)
}

func (a *App) agentInput(inbound message.Inbound) agent.Input {
	return agent.Input{
		Text: inbound.Text, ImageURLs: append([]string(nil), inbound.ImageURLs...),
		Identity: legacyIdentity(inbound),
		SendUpdate: func(ctx context.Context, _ store.Identity, update string) error {
			outcome := a.deliverContent(ctx, inbound, "agent_update", message.Content{Text: update}, 0)
			if outcome.State == delivery.OutcomeAccepted {
				return nil
			}
			if outcome.Err != nil {
				return outcome.Err
			}
			return fmt.Errorf("immediate delivery %s", outcome.State)
		},
		SendResponse: func(ctx context.Context, _ store.Identity, response commands.Response) error {
			if err := a.deliverResponseChecked(ctx, inbound, response); err != nil {
				return fmt.Errorf("deliver host command response: %w", err)
			}
			return nil
		},
	}
}

func (a *App) deliverResponse(ctx context.Context, inbound message.Inbound, response commands.Response) {
	if err := a.deliverResponseChecked(ctx, inbound, response); err != nil {
		a.logf("immediate reply failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			inbound.Conversation.Platform, inbound.Conversation.Type, inbound.Conversation.ID, err)
	}
}

func (a *App) deliverResponseChecked(ctx context.Context, inbound message.Inbound, response commands.Response) error {
	parts := response.Parts
	if len(parts) == 0 {
		parts = []commands.Response{response}
	}
	attempt := 0
	for _, part := range parts {
		outcome, attempts := a.deliverResponsePart(ctx, inbound, part, attempt)
		attempt += attempts
		if outcome.State != delivery.OutcomeAccepted {
			if outcome.Err != nil {
				return outcome.Err
			}
			return fmt.Errorf("delivery state=%s code=%s", outcome.State, outcome.Code)
		}
	}
	return nil
}

func (a *App) recordIgnored(ctx context.Context, inbound message.Inbound) {
	a.record(ctx, inbound, store.Interaction{
		RawText: inbound.Text, Handled: false, Status: store.InteractionStatusIgnored,
	}, "ignored")
}

func (a *App) recordOutbound(ctx context.Context, inbound message.Inbound, text string, outcome delivery.Outcome) {
	status := store.InteractionStatusFailed
	switch outcome.State {
	case delivery.OutcomeAccepted:
		status = store.InteractionStatusAccepted
	case delivery.OutcomeUnknown:
		status = store.InteractionStatusUnknown
	}
	errText := ""
	if outcome.Err != nil {
		errText = outcome.Err.Error()
	}
	a.record(ctx, inbound, store.Interaction{
		Direction: store.InteractionDirectionOutbound, RawText: text, Handled: true, Status: status,
		Error: errText, PlatformMessageID: outcome.Receipt.PlatformMessageID,
		DeliveryMethod: outcome.Receipt.DeliveryMethod, SourceMessageID: outcome.Receipt.SourceMessageID,
		AcceptedAt: outcome.Receipt.AcceptedAt,
	}, "outbound")
}

func (a *App) record(ctx context.Context, inbound message.Inbound, interaction store.Interaction, label string) {
	if a.recorder == nil {
		return
	}
	if err := a.recorder.RecordInteraction(ctx, legacyIdentity(inbound), interaction); err != nil {
		a.logf("record %s interaction failed: %v", label, err)
	}
}

func legacyIdentity(inbound message.Inbound) store.Identity {
	return store.Identity{
		Platform: inbound.Actor.Platform, UserID: inbound.Actor.UserID,
		ConversationType: inbound.Conversation.Type, ConversationID: inbound.Conversation.ID,
	}
}

func validateInbound(inbound message.Inbound) error {
	if strings.TrimSpace(inbound.Actor.Platform) == "" || strings.TrimSpace(inbound.Actor.UserID) == "" {
		return errors.New("inbound actor is incomplete")
	}
	if strings.TrimSpace(inbound.Conversation.Platform) == "" || strings.TrimSpace(inbound.Conversation.Type) == "" || strings.TrimSpace(inbound.Conversation.ID) == "" {
		return errors.New("inbound conversation is incomplete")
	}
	if !strings.EqualFold(strings.TrimSpace(inbound.Actor.Platform), strings.TrimSpace(inbound.Conversation.Platform)) {
		return errors.New("inbound actor and conversation platforms differ")
	}
	return nil
}

func (a *App) logf(format string, args ...any) {
	if a.logger != nil {
		a.logger.Printf(format, args...)
	}
}

var _ CommandHandler = commands.Handler{}
var _ AgentHandler = (*agent.Service)(nil)
var _ Dispatcher = (*agent.Dispatcher)(nil)
var _ Deliverer = (*delivery.Service)(nil)
var _ Recorder = (*store.Store)(nil)
var _ Renderer = responses.Renderer{}
var _ Processor = (*App)(nil)
