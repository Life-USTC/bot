package agent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestHasCalendarSubscriptionURL(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{text: "https://life.example/ical/user.ics?sig=made-up", want: true},
		{text: "[订阅](https://life.example/api/calendar-feeds/user:token.ics)", want: true},
		{text: "webcal://life.example/private/calendar.ics", want: true},
		{text: "https://life.example/ical/user?sig=made-up", want: true},
		{text: "登录：https://life.example/oauth/device", want: false},
	}
	for _, test := range tests {
		if got := hasCalendarSubscriptionURL(test.text); got != test.want {
			t.Fatalf("hasCalendarSubscriptionURL(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestCalendarSubscriptionPromptUsesHostCommand(t *testing.T) {
	instruction := currentInstructionAt(time.Now())
	for _, expected := range []string{
		"handled only by the host command 订阅 链接",
		"workspace_calendar_feed_get intentionally does not expose",
		"Never create, infer, reconstruct",
	} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("instruction is missing %q", expected)
		}
	}
}

func TestSendMessagePartBlocksCalendarSubscriptionURL(t *testing.T) {
	sent := false
	_, err := sendMessagePart(context.Background(), store.Identity{}, func(context.Context, store.Identity, string) error {
		sent = true
		return nil
	}, messagePartInput{Content: "先订阅：https://life.example/ical/user.ics?sig=made-up"})
	if !errors.Is(err, errUnverifiedCalendarURL) || sent {
		t.Fatalf("err = %v, sent = %v", err, sent)
	}
}

func TestHandleResponseBlocksModelGeneratedCalendarURL(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"订阅链接：https://life.example/ical/user.ics?sig=made-up"},"finish_reason":"stop"}]
		}`))
	}))
	t.Cleanup(modelServer.Close)

	var logs bytes.Buffer
	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  "test-key",
		BaseURL: modelServer.URL,
		Model:   "test-model",
		Logger:  log.New(&logs, "", 0),
	}, commands.Handler{}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}

	response, handled := svc.HandleResponse(context.Background(), Input{
		Text: "给我课表的日历订阅链接",
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "private",
			ConversationID:   "42",
		},
	})
	if !handled || response.Text != calendarURLGuardReply {
		t.Fatalf("response = %#v, handled = %v", response, handled)
	}
	if !strings.Contains(logs.String(), "reason=unverified_calendar_url") {
		t.Fatalf("logs = %q", logs.String())
	}
}
