package delivery

import (
	"context"
	"errors"
	"testing"

	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
)

type testAdapter struct {
	platform string
	got      message.Outbound
	outcome  Outcome
}

type testRenderer struct {
	data  []byte
	err   error
	calls int
}

func (r *testRenderer) RenderPNGContext(_ context.Context, _ *responses.Image) ([]byte, int, int, error) {
	r.calls++
	return append([]byte(nil), r.data...), 1, 1, r.err
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
		TextPolicy: message.TextPolicyLLM,
		Target:     message.Conversation{Platform: " QQBOT ", Type: "private", ID: "42"},
		Content:    message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
	}
	if outcome := service.DeliverNow(context.Background(), outbound); outcome.State != OutcomeAccepted {
		t.Fatalf("outcome = %#v", outcome)
	}
	if qq.got.Content.TextContent() != "hello" || napcat.got.Content.HasContent() {
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
		TextPolicy: message.TextPolicyLLM,
		Target:     message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Content:    message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
	})
	if outcome.State != OutcomeRejected || outcome.Code != "unsupported_platform" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if qq.got.Content.HasContent() {
		t.Fatalf("message was sent to the wrong adapter: %#v", qq.got)
	}
}

func TestServiceRejectsDuplicateAdapters(t *testing.T) {
	_, err := New(nil, &testAdapter{platform: "qqbot"}, &testAdapter{platform: " QQBOT "})
	if err == nil {
		t.Fatal("expected duplicate adapter error")
	}
}

func TestServiceRegistersAdapterDuringComposition(t *testing.T) {
	service, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{platform: "napcat", outcome: Outcome{State: OutcomeAccepted}}
	if err := service.Register(adapter); err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		TextPolicy: message.TextPolicyLLM,
		Target:     message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Content:    message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
	})
	if outcome.State != OutcomeAccepted {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestServiceNormalizesInvalidAdapterOutcome(t *testing.T) {
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{Err: errors.New("broken")}}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		TextPolicy: message.TextPolicyLLM,
		Target:     message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content:    message.Content{Parts: []message.ContentPart{{Text: "hello"}}},
	})
	if outcome.State != OutcomeUnknown || outcome.Code != "invalid_adapter_outcome" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestServiceRendersHostTextAtDeliveryBoundary(t *testing.T) {
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	renderer := &testRenderer{data: []byte("png")}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRenderer(renderer); err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		Kind: "error", Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content: message.Content{Parts: []message.ContentPart{{Text: "失败详情"}}},
	})
	if outcome.State != OutcomeAccepted {
		t.Fatalf("outcome = %#v", outcome)
	}
	if renderer.calls != 1 {
		t.Fatalf("renderer calls = %d, want 1", renderer.calls)
	}
	if adapter.got.Content.TextContent() != "失败详情" {
		t.Fatalf("adapter content text = %q", adapter.got.Content.TextContent())
	}
	if len(adapter.got.Content.Parts) != 1 || adapter.got.Content.Parts[0].Attachment == nil || len(adapter.got.Content.Parts[0].Attachment.Data) == 0 {
		t.Fatalf("adapter content = %#v", adapter.got.Content)
	}
	if adapter.got.Content.Parts[0].Text != "" {
		t.Fatalf("host text reached adapter: %#v", adapter.got.Content.Parts[0])
	}
}

func TestServiceRenderFailureIsRetryableWithoutAdapterCall(t *testing.T) {
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	renderer := &testRenderer{err: errors.New("render down")}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRenderer(renderer); err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content: message.Content{Parts: []message.ContentPart{{Text: "稍后重试"}}},
	})
	if outcome.State != OutcomeRetryable || outcome.Code != "render_failed" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if adapter.got.Content.HasContent() {
		t.Fatalf("adapter received output after render failure: %#v", adapter.got)
	}
}

func TestServicePreservesExplicitLLMText(t *testing.T) {
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		TextPolicy: message.TextPolicyLLM,
		Target:     message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content:    message.Content{Parts: []message.ContentPart{{Text: "模型回答"}}},
	})
	if outcome.State != OutcomeAccepted || adapter.got.Content.TextContent() != "模型回答" {
		t.Fatalf("outcome = %#v adapter = %#v", outcome, adapter.got)
	}
}

func TestServiceRendersPersistedStructuredImageIntent(t *testing.T) {
	image := responses.NewRichTextImage("notification", "# 通知\n\n提醒内容", "提醒内容")
	payload, err := responses.EncodeImageIntent(image)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{platform: "qqbot", outcome: Outcome{State: OutcomeAccepted}}
	renderer := &testRenderer{data: []byte("png")}
	service, err := New(nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRenderer(renderer); err != nil {
		t.Fatal(err)
	}
	outcome := service.DeliverNow(context.Background(), message.Outbound{
		Target: message.Conversation{Platform: "qqbot", Type: "private", ID: "42"},
		Content: message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{
			MIMEType: "image/png", AltText: image.AltText, RenderPayload: payload,
		}}}},
	})
	if outcome.State != OutcomeAccepted || renderer.calls != 1 {
		t.Fatalf("outcome = %#v renderer calls = %d", outcome, renderer.calls)
	}
	attachment := adapter.got.Content.Parts[0].Attachment
	if attachment == nil || string(attachment.Data) != "png" || len(attachment.RenderPayload) != 0 || attachment.AltText != image.AltText {
		t.Fatalf("rendered attachment = %#v", attachment)
	}
}
