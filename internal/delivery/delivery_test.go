package delivery

import (
	"context"
	"errors"
	"testing"

	"github.com/Life-USTC/Bot/internal/message"
)

type testAdapter struct {
	platform string
	got      message.Outbound
	outcome  Outcome
}

func (a *testAdapter) Platform() string { return a.platform }

func (a *testAdapter) Deliver(_ context.Context, outbound message.Outbound) Outcome {
	a.got = outbound
	return a.outcome
}

func TestServiceRoutesOnlyToExactPlatform(t *testing.T) {
	qq := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	napcat := &testAdapter{platform: "napcat", outcome: Outcome{State: OutcomeAccepted}}
	service, err := New(nil, qq, napcat)
	if err != nil {
		t.Fatal(err)
	}
	outbound := message.Outbound{
		Target:  message.Conversation{Platform: " QQBOT ", Type: "private", ID: "42"},
		Content: message.Content{Text: "hello"},
	}
	if outcome := service.DeliverNow(context.Background(), outbound); outcome.State != OutcomeAccepted {
		t.Fatalf("outcome = %#v", outcome)
	}
	if qq.got.Content.Text != "hello" || napcat.got.Content.Text != "" {
		t.Fatalf("qq = %#v napcat = %#v", qq.got, napcat.got)
	}
}

func TestServiceDoesNotGuessSinglePlatform(t *testing.T) {
	qq := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	service, err := New(nil, qq)
	if err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Content: message.Content{Text: "hello"},
	})
	if outcome.State != OutcomeRejected || outcome.Code != "unsupported_platform" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if qq.got.Content.Text != "" {
		t.Fatalf("message was sent to the wrong adapter: %#v", qq.got)
	}
}

func TestServiceRejectsDuplicateAdapters(t *testing.T) {
	_, err := New(nil, &testAdapter{platform: "qqbot"}, &testAdapter{platform: " QQBOT "})
	if err == nil {
		t.Fatal("expected duplicate adapter error")
	}
}

func TestServiceNormalizesInvalidAdapterOutcome(t *testing.T) {
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{Err: errors.New("broken")}}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content: message.Content{Text: "hello"},
	})
	if outcome.State != OutcomeUnknown || outcome.Code != "invalid_adapter_outcome" {
		t.Fatalf("outcome = %#v", outcome)
	}
}
