package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type recurringDeliveryAdapter struct {
	got []message.Outbound
}

func (a *recurringDeliveryAdapter) Platform() string { return "napcat" }
func (a *recurringDeliveryAdapter) Deliver(_ context.Context, outbound message.Outbound) delivery.Outcome {
	a.got = append(a.got, outbound)
	return delivery.Outcome{State: delivery.OutcomeAccepted}
}

type recurringDeliveryRenderer struct {
	err          error
	beforeReturn func()
}

func (r *recurringDeliveryRenderer) RenderPNGContext(_ context.Context, _ *responses.Image) ([]byte, int, int, error) {
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	return []byte("rendered-png"), 10, 10, r.err
}

func TestRecurringReminderDeliveryRendersAndCancelsAtBoundary(t *testing.T) {
	for _, mode := range []string{"image", "render-failure", "pause-while-rendering"} {
		t.Run(mode, func(t *testing.T) {
			s, err := store.Open(t.TempDir() + "/recurring.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ident := store.Identity{Platform: "napcat", UserID: "alice", ConversationType: "private", ConversationID: "alice"}
			now := time.Now().UTC()
			scheduled := now.Truncate(time.Minute)
			rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: scheduled.Format("15:04"), Timezone: "UTC"}, scheduled.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.EnqueueDueRecurringReminders(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			records, err := s.ClaimDue(t.Context(), now, 10)
			if err != nil || len(records) != 1 || records[0].Message.TextPolicy != message.TextPolicyImageOnly {
				t.Fatalf("records=%#v err=%v", records, err)
			}
			adapter := &recurringDeliveryAdapter{}
			service, err := delivery.New(s, adapter)
			if err != nil {
				t.Fatal(err)
			}
			renderer := &recurringDeliveryRenderer{}
			if mode == "render-failure" {
				renderer.err = errors.New("renderer unavailable")
			}
			if mode == "pause-while-rendering" {
				renderer.beforeReturn = func() {
					if _, err := s.PauseRecurringReminder(t.Context(), ident, rule.ID, true, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := service.SetRenderer(renderer); err != nil {
				t.Fatal(err)
			}
			outcome := service.DeliverRecord(t.Context(), records[0])
			switch mode {
			case "image":
				if outcome.State != delivery.OutcomeAccepted || len(adapter.got) != 1 {
					t.Fatalf("outcome=%#v sent=%#v", outcome, adapter.got)
				}
				if content := adapter.got[0].Content; content.ExplicitTextContent() != "" || len(content.Parts) != 1 || content.Parts[0].Attachment == nil || content.Parts[0].Attachment.MIMEType != "image/png" || len(content.Parts[0].Attachment.Data) == 0 {
					t.Fatalf("delivery was not image-only: %#v", content)
				}
			case "render-failure":
				if outcome.State != delivery.OutcomeRetryable || outcome.Code != "render_failed" || len(adapter.got) != 0 {
					t.Fatalf("renderer failure fell back to text: %#v sent=%#v", outcome, adapter.got)
				}
			case "pause-while-rendering":
				if outcome.State != delivery.OutcomeRejected || outcome.Code != "superseded" || len(adapter.got) != 0 {
					t.Fatalf("cancelled reminder sent: %#v sent=%#v", outcome, adapter.got)
				}
			}
		})
	}
}
