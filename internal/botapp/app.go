package botapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

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
	Commands   CommandHandler
	Agent      AgentHandler
	Dispatcher Dispatcher
	Delivery   Deliverer
	Recorder   Recorder
	Renderer   Renderer
	Logger     *log.Logger
}

// App is the sole application layer for inbound bot messages. Protocol
// adapters translate events into message.Inbound and hand them to Process.
type App struct {
	commands   CommandHandler
	agent      AgentHandler
	dispatcher Dispatcher
	delivery   Deliverer
	recorder   Recorder
	renderer   Renderer
	logger     *log.Logger
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
		logger: config.Logger,
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
			outcome := a.deliver(ctx, inbound, commands.Response{Text: update, Kind: "agent_update"}, 0)
			if outcome.State == delivery.OutcomeAccepted {
				return nil
			}
			if outcome.Err != nil {
				return outcome.Err
			}
			return fmt.Errorf("immediate delivery %s", outcome.State)
		},
	}
}

func (a *App) deliverResponse(ctx context.Context, inbound message.Inbound, response commands.Response) {
	parts := response.Parts
	if len(parts) == 0 {
		parts = []commands.Response{response}
	}
	for index, part := range parts {
		outcome := a.deliver(ctx, inbound, part, index)
		if outcome.State != delivery.OutcomeAccepted {
			a.logf("immediate reply failed: platform=%s conversation_type=%s conversation_id=%s state=%s code=%s error=%v",
				inbound.Conversation.Platform, inbound.Conversation.Type, inbound.Conversation.ID,
				outcome.State, outcome.Code, outcome.Err)
			return
		}
	}
}

func (a *App) deliver(ctx context.Context, inbound message.Inbound, response commands.Response, partIndex int) delivery.Outcome {
	content := message.Content{Text: response.Text}
	if response.Image != nil {
		attachment, err := a.renderAttachment(response.Image)
		if err != nil {
			a.logf("render immediate reply failed: %v", err)
		} else {
			content.Attachment = attachment
		}
	}
	reply := inbound.Source
	if partIndex > 0 || reply.Sequence <= 0 {
		reply.Sequence = partIndex + 1
	}
	outbound := message.Outbound{
		Kind: response.Kind, Target: inbound.Conversation, ReplyTo: &reply, Content: content,
	}
	outcome := a.delivery.DeliverNow(ctx, outbound)
	a.recordOutbound(ctx, inbound, content.Text, outcome)
	return outcome
}

func (a *App) renderAttachment(image *responses.Image) (*message.Attachment, error) {
	if image == nil {
		return nil, nil
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		return &message.Attachment{MIMEType: "image/png", URL: url}, nil
	}
	if a.renderer == nil {
		return nil, errors.New("response renderer is unavailable")
	}
	data, _, _, err := a.renderer.RenderPNG(image)
	if err != nil {
		return nil, err
	}
	return &message.Attachment{MIMEType: "image/png", Data: data}, nil
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
