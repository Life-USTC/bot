package botapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
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

type dispatcherSpy struct {
	input    agent.Input
	callback agent.DispatchCallback
	called   int
}

func (s *dispatcherSpy) Submit(input agent.Input, callback agent.DispatchCallback) {
	s.input = input
	s.callback = callback
	s.called++
}

type deliverySpy struct {
	messages []message.Outbound
	outcomes []delivery.Outcome
}

func (s *deliverySpy) DeliverNow(_ context.Context, outbound message.Outbound) delivery.Outcome {
	s.messages = append(s.messages, outbound)
	if len(s.outcomes) > 0 {
		outcome := s.outcomes[0]
		s.outcomes = s.outcomes[1:]
		return outcome
	}
	return delivery.Outcome{State: delivery.OutcomeAccepted, Receipt: message.Receipt{
		PlatformMessageID: "accepted-1", DeliveryMethod: "immediate", AcceptedAt: time.Now(),
	}}
}

type rendererFunc func(*responses.Image) ([]byte, int, int, error)

func (fn rendererFunc) RenderPNG(image *responses.Image) ([]byte, int, int, error) {
	return fn(image)
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

func TestResumedHostDeliveredResponseCompletesWithoutDuplicateDelivery(t *testing.T) {
	deliverer := &deliverySpy{}
	recorder := &recorderSpy{}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.SendResponse(ctx, input.Identity, commands.Response{Text: "private-link", Kind: "subscription"}); err != nil {
				t.Fatalf("host delivery: %v", err)
			}
			return commands.Response{Kind: commands.ResponseKindHostDelivered}, true
		}),
		Delivery: deliverer, Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	err = app.ResumePendingRequest(context.Background(), store.PendingRequest{Identity: ident, Text: "给我日历订阅链接"})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliverer.messages) != 1 || deliverer.messages[0].Content.Text != "private-link" {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
}

func TestResumePendingRequestWaitsForExecutionAndDelivery(t *testing.T) {
	dispatcher := &dispatcherSpy{}
	deliverer := &deliverySpy{}
	var resumed agent.Input
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(_ context.Context, input agent.Input) (commands.Response, bool) {
			resumed = input
			return commands.Response{Text: "明天没有课。", Kind: "agent"}, true
		}),
		Dispatcher: dispatcher,
		Delivery:   deliverer,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := store.PendingRequest{
		Identity: store.Identity{Platform: " napcat ", UserID: " 42 ", ConversationType: "private", ConversationID: "42"},
		Text:     "  查询明天课表  ",
	}
	if err := app.ResumePendingRequest(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if dispatcher.called != 0 {
		t.Fatalf("dispatcher calls = %d", dispatcher.called)
	}
	if resumed.Text != pending.Text || resumed.Identity != pending.Identity {
		t.Fatalf("resumed input = %#v", resumed)
	}
	if len(resumed.ImageURLs) != 0 {
		t.Fatalf("resumed request unexpectedly included images: %#v", resumed.ImageURLs)
	}
	if len(deliverer.messages) != 1 || deliverer.messages[0].Content.Text != "明天没有课。" {
		t.Fatalf("resumed delivery = %#v", deliverer.messages)
	}
}

func TestResumePendingRequestRejectsNonPrivateOrIncompleteRequests(t *testing.T) {
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) { return commands.Response{}, false }),
		Delivery: &deliverySpy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range []store.PendingRequest{
		{Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "g"}, Text: "查询"},
		{Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}},
	} {
		if err := app.ResumePendingRequest(context.Background(), pending); err == nil {
			t.Fatalf("invalid pending request accepted: %#v", pending)
		}
	}
}

func TestImageReplySendsOnlyImageWhenAccepted(t *testing.T) {
	deliverer := &deliverySpy{}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "fallback", Image: responses.NewTextImage("bus", "校车", "fallback"), Kind: "bus"}, true
		}),
		Delivery: deliverer,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return []byte("png"), 1, 1, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("napcat", "42", "校车"))

	if len(deliverer.messages) != 1 {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
	content := deliverer.messages[0].Content
	if content.Text != "" || content.Attachment == nil || string(content.Attachment.Data) != "png" {
		t.Fatalf("content = %#v", content)
	}
}

func TestImageRenderFailureFallsBackToText(t *testing.T) {
	deliverer := &deliverySpy{}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "fallback", Image: responses.NewTextImage("bus", "校车", "fallback"), Kind: "bus"}, true
		}),
		Delivery: deliverer,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return nil, 0, 0, errors.New("render failed")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("napcat", "42", "校车"))

	if len(deliverer.messages) != 1 || deliverer.messages[0].Content.Text != "fallback" || deliverer.messages[0].Content.Attachment != nil {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
}

func TestImageRenderTimeoutFallsBackToText(t *testing.T) {
	deliverer := &deliverySpy{}
	unblock := make(chan struct{})
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "fallback", Image: responses.NewTextImage("bus", "校车", "fallback"), Kind: "bus"}, true
		}),
		Delivery: deliverer, ImageRenderTimeout: 10 * time.Millisecond,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			<-unblock
			return []byte("late"), 1, 1, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("napcat", "42", "校车"))
	close(unblock)

	if len(deliverer.messages) != 1 || deliverer.messages[0].Content.Text != "fallback" {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
}

func TestRejectedImageDeliveryFallsBackToText(t *testing.T) {
	deliverer := &deliverySpy{outcomes: []delivery.Outcome{
		{State: delivery.OutcomeRejected, Code: "invalid_attachment", Err: errors.New("rejected")},
		{State: delivery.OutcomeAccepted},
	}}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "fallback", Image: responses.NewTextImage("bus", "校车", "fallback"), Kind: "bus"}, true
		}),
		Delivery: deliverer,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return []byte("png"), 1, 1, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("qqbot", "42", "校车"))

	if len(deliverer.messages) != 2 || deliverer.messages[0].Content.Text != "" ||
		deliverer.messages[0].Content.Attachment == nil || deliverer.messages[1].Content.Text != "fallback" ||
		deliverer.messages[1].Content.Attachment != nil || deliverer.messages[1].ReplyTo.Sequence != deliverer.messages[0].ReplyTo.Sequence+1 {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
}

func TestUnknownImageDeliveryDoesNotRiskDuplicateFallback(t *testing.T) {
	deliverer := &deliverySpy{outcomes: []delivery.Outcome{{State: delivery.OutcomeUnknown}}}
	app, err := New(Config{
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "fallback", Image: responses.NewTextImage("bus", "校车", "fallback"), Kind: "bus"}, true
		}),
		Delivery: deliverer,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return []byte("png"), 1, 1, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Process(context.Background(), privateInbound("qqbot", "42", "校车"))

	if len(deliverer.messages) != 1 || deliverer.messages[0].Content.Attachment == nil {
		t.Fatalf("deliveries = %#v", deliverer.messages)
	}
}

func privateInbound(platform, id, text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: platform, UserID: id},
		Conversation: message.Conversation{Platform: platform, Type: "private", ID: id}, Text: text,
	}
}
