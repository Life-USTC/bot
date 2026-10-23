package napcat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

const sendEventPrefix = "EventChecker Failed: NTEvent serviceAndMethod:NodeIKernelMsgService/sendMsg ListenerName:NodeIKernelMsgListener/onMsgInfoListUpdate EventRet:\n"

func TestNapCatMediaFailureRequiresExplicitKernelRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want delivery.OutcomeState
	}{
		{"media rejected", sendEventPrefix + `{"result":-1,"errMsg":"rich media transfer failed"}`, delivery.OutcomeRetryable},
		{"wording alone", "rich media transfer failed", delivery.OutcomeRejected},
		{"missing result", sendEventPrefix + `{"errMsg":"rich media transfer failed"}`, delivery.OutcomeRejected},
		{"zero result", sendEventPrefix + `{"result":0,"errMsg":"rich media transfer failed"}`, delivery.OutcomeRejected},
		{"null result", sendEventPrefix + `{"result":null,"errMsg":"rich media transfer failed"}`, delivery.OutcomeRejected},
		{"malformed result", sendEventPrefix + `{"result":"-1","errMsg":"rich media transfer failed"}`, delivery.OutcomeRejected},
		{"invalid JSON", sendEventPrefix + `{"result":-1`, delivery.OutcomeRejected},
		{"trailing text", sendEventPrefix + `{"result":-1,"errMsg":"rich media transfer failed"} extra`, delivery.OutcomeRejected},
		{"other rejection", sendEventPrefix + `{"result":-1,"errMsg":"bad target"}`, delivery.OutcomeRejected},
		{"other service", strings.ReplaceAll(sendEventPrefix, "sendMsg", "forwardMsg") + `{"result":-1,"errMsg":"rich media transfer failed"}`, delivery.OutcomeRejected},
		{"kernel timeout", strings.Replace(sendEventPrefix, "EventChecker Failed:", "Timeout:", 1) + `{"result":0}`, delivery.OutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, retCode := range []int{0, 200, 1200} {
				err := napcatActionError(napcatActionResponse{Status: "failed", RetCode: retCode, Message: tc.text})
				var rejected napcatActionRejectedError
				if !errors.As(err, &rejected) || rejected.retCode != retCode {
					t.Fatalf("lost retcode %d: %v", retCode, err)
				}
				outcome := classifyNapCatDeliveryError(fmt.Errorf("send: %w", err))
				if outcome.State != tc.want {
					t.Fatalf("outcome=%#v want=%s", outcome, tc.want)
				}
				if tc.want == delivery.OutcomeRetryable && (outcome.Code != "media_transfer_failed" || outcome.Err.Error() != fmt.Sprintf("napcat media transfer failed: retcode=%d result=-1", retCode)) {
					t.Fatalf("diagnostic=%#v", outcome)
				}
			}
		})
	}
}

func TestNapCatMediaRetryUsesDurableOutboxAndExpiry(t *testing.T) {
	for _, expires := range []bool{false, true} {
		t.Run(fmt.Sprintf("expires=%v", expires), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"status": "failed", "retcode": 200,
						"message": sendEventPrefix + `{"result":-1,"errMsg":"rich media transfer failed"}`,
					})
					return
				}
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":"accepted"}}`))
			}))
			defer server.Close()
			path := t.TempDir() + "/bot.db"
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			adapter := NewDeliveryAdapter(&Bridge{APIURL: server.URL, HTTPClient: server.Client()})
			service, err := delivery.New(db, adapter)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Add(time.Minute)
			outbound := message.Outbound{
				Kind: "notification.class", TextPolicy: message.TextPolicyImageOnly,
				Target:    message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
				Content:   message.Content{Parts: []message.ContentPart{{Attachment: &message.Attachment{MIMEType: "image/png", Data: []byte("png")}}}},
				DedupeKey: "class:one-occurrence", ExpiresAt: now.Add(20 * time.Second),
			}
			first, created, err := service.Enqueue(t.Context(), outbound)
			if err != nil || !created {
				t.Fatalf("enqueue created=%v err=%v", created, err)
			}
			claimed, err := db.ClaimDue(t.Context(), now, 1)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("claim=%#v err=%v", claimed, err)
			}
			outcome := service.DeliverRecord(t.Context(), claimed[0])
			if outcome.State != delivery.OutcomeRetryable || calls.Load() != 1 {
				t.Fatalf("first outcome=%#v calls=%d", outcome, calls.Load())
			}
			next := now.Add(5 * time.Second)
			if err := db.Complete(t.Context(), first.ID, outcome, next); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			service, err = delivery.New(db, adapter)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, created, err := service.Enqueue(t.Context(), outbound)
			if err != nil || created || duplicate.ID != first.ID || duplicate.Status != delivery.StatusRetryWait || duplicate.Attempts != 1 {
				t.Fatalf("persisted=%#v created=%v err=%v", duplicate, created, err)
			}
			if early, err := db.ClaimDue(t.Context(), next.Add(-time.Second), 1); err != nil || len(early) != 0 {
				t.Fatalf("early retry=%#v err=%v", early, err)
			}
			if expires {
				next = outbound.ExpiresAt
				if err := db.ExpireDue(t.Context(), next); err != nil {
					t.Fatal(err)
				}
			}
			claimed, err = db.ClaimDue(t.Context(), next, 1)
			if err != nil {
				t.Fatal(err)
			}
			if expires {
				if len(claimed) != 0 || calls.Load() != 1 {
					t.Fatalf("expired retry=%#v calls=%d", claimed, calls.Load())
				}
			} else {
				if len(claimed) != 1 || claimed[0].ID != first.ID || claimed[0].Attempts != 2 {
					t.Fatalf("retry=%#v", claimed)
				}
				outcome = service.DeliverRecord(t.Context(), claimed[0])
				if outcome.State != delivery.OutcomeAccepted || calls.Load() != 2 {
					t.Fatalf("retry outcome=%#v calls=%d", outcome, calls.Load())
				}
				if err := db.Complete(t.Context(), first.ID, outcome, time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			if again, err := db.ClaimDue(t.Context(), next.Add(time.Minute), 1); err != nil || len(again) != 0 {
				t.Fatalf("terminal message repeated=%#v err=%v", again, err)
			}
		})
	}
}
