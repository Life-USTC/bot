package botapp

import (
	"context"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

type commandFunc func(context.Context, commands.Input) (commands.Response, bool)

func (fn commandFunc) HandleResponse(ctx context.Context, input commands.Input) (commands.Response, bool) {
	return fn(ctx, input)
}

type agentFunc func(context.Context, agent.Input) (commands.Response, bool)

func (fn agentFunc) HandleResponse(ctx context.Context, input agent.Input) (commands.Response, bool) {
	return fn(ctx, input)
}

type deliverySpy struct {
	messages []message.Outbound
}

func (s *deliverySpy) DeliverNow(_ context.Context, outbound message.Outbound) delivery.Outcome {
	s.messages = append(s.messages, outbound)
	return delivery.Outcome{State: delivery.OutcomeAccepted, Receipt: message.Receipt{
		PlatformMessageID: "accepted-1", DeliveryMethod: "immediate", AcceptedAt: time.Now(),
	}}
}

type recordedInteraction struct {
	identity    store.Identity
	interaction store.Interaction
}

type recorderSpy struct {
	entries []recordedInteraction
}

func (s *recorderSpy) RecordInteraction(_ context.Context, identity store.Identity, interaction store.Interaction) error {
	s.entries = append(s.entries, recordedInteraction{identity: identity, interaction: interaction})
	return nil
}

func TestGroupInboundKeepsActorSeparateFromConversation(t *testing.T) {
	deliverer := &deliverySpy{}
	recorder := &recorderSpy{}
	var commandIdentity store.Identity
	app, err := New(Config{
		Commands: commandFunc(func(_ context.Context, input commands.Input) (commands.Response, bool) {
			commandIdentity = input.Identity
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(_ context.Context, input agent.Input) (commands.Response, bool) {
			if input.Identity.UserID != "member-7" || input.Identity.ConversationID != "group-42" {
				t.Fatalf("agent identity = %#v", input.Identity)
			}
			return commands.Response{Text: "完成", Kind: "agent"}, true
		}),
		Delivery: deliverer, Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := message.Inbound{
		Actor:        message.Actor{Platform: "qqbot", UserID: "member-7"},
		Conversation: message.Conversation{Platform: "qqbot", Type: "group", ID: "group-42"},
		Source:       message.ReplyRef{MessageID: "source-1"}, Text: "帮我查一下", BotMentioned: true,
	}
	app.Process(context.Background(), inbound)

	if commandIdentity.UserID != "member-7" || commandIdentity.ConversationID != "group-42" || !store.IsGroupConversation(commandIdentity) {
		t.Fatalf("command identity = %#v", commandIdentity)
	}
	if len(deliverer.messages) != 1 || deliverer.messages[0].Target.ID != "group-42" {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
	if len(recorder.entries) != 2 {
		t.Fatalf("recorded interactions = %#v", recorder.entries)
	}
	if recorder.entries[0].interaction.Command != "agent" || recorder.entries[1].interaction.Status != store.InteractionStatusAccepted {
		t.Fatalf("recorded interactions = %#v", recorder.entries)
	}
}

func TestAcceptedImmediateReplyIsRecordedExactlyOnce(t *testing.T) {
	deliverer := &deliverySpy{}
	recorder := &recorderSpy{}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "command"}, true
		}),
		Delivery: deliverer, Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("napcat", "42", "ping"))

	if len(deliverer.messages) != 1 {
		t.Fatalf("delivery count = %d", len(deliverer.messages))
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("acceptance record count = %d", len(recorder.entries))
	}
	got := recorder.entries[0].interaction
	if got.Direction != store.InteractionDirectionOutbound || got.Status != store.InteractionStatusAccepted || got.PlatformMessageID != "accepted-1" {
		t.Fatalf("acceptance record = %#v", got)
	}
}

func TestIgnoredInboundIsRecordedOnceWithoutDelivery(t *testing.T) {
	deliverer := &deliverySpy{}
	recorder := &recorderSpy{}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Delivery: deliverer, Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("qqbot", "u-1", "unknown"))
	if len(deliverer.messages) != 0 || len(recorder.entries) != 1 || recorder.entries[0].interaction.Status != store.InteractionStatusIgnored {
		t.Fatalf("deliveries=%d interactions=%#v", len(deliverer.messages), recorder.entries)
	}
}

func privateInbound(platform, id, text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: platform, UserID: id},
		Conversation: message.Conversation{Platform: platform, Type: "private", ID: id}, Text: text,
	}
}
