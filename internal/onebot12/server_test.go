package onebot12

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	libob "github.com/botuniverse/go-libonebot"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestIdentityFromParamsDefaultsOptionalFields(t *testing.T) {
	ident, ok := identityFromParams(libob.ResponseWriter{}, &libob.Request{
		Params: libob.EasierMapFromMap(map[string]interface{}{
			"user_id": "42",
		}),
	})
	if !ok {
		t.Fatal("identity was not parsed")
	}
	want := store.Identity{
		Platform:         "onebot",
		UserID:           "42",
		ConversationType: "private",
		ConversationID:   "42",
	}
	if ident != want {
		t.Fatalf("identity = %#v, want %#v", ident, want)
	}
}

func TestStatusWithoutLifeClientReportsOffline(t *testing.T) {
	server := New(Config{SelfID: "bot"}, nil)
	resp := server.onebot.CallAction(libob.ActionGetStatus, nil)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, message = %q", resp.Status, resp.Message)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", resp.Data)
	}
	if data["good"] != false || data["online"] != false {
		t.Fatalf("data = %#v", data)
	}
}

func TestLifeActionWithoutLifeClientFails(t *testing.T) {
	server := New(Config{SelfID: "bot"}, nil)
	resp := server.onebot.CallAction(actionPrefix+".get_current_semester", nil)
	if resp.Status != "failed" || resp.RetCode != libob.RetCodeUnsupportedAction {
		t.Fatalf("response = %#v", resp)
	}
	if !strings.Contains(resp.Message, "Life @ USTC API is not configured") {
		t.Fatalf("message = %q", resp.Message)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestErrorRetCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: libob.RetCodeNetworkError},
		{name: "handler timeout", err: http.ErrHandlerTimeout, want: libob.RetCodeNetworkError},
		{name: "net timeout", err: timeoutError{}, want: libob.RetCodeNetworkError},
		{name: "regular", err: errors.New("boom"), want: libob.RetCodeInternalHandlerError},
	}
	for _, tt := range tests {
		if got := errorRetCode(tt.err); got != tt.want {
			t.Fatalf("%s: errorRetCode = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestContextWithTimeoutSetsDeadline(t *testing.T) {
	ctx, cancel := ContextWithTimeout()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("deadline was not set")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 15*time.Second {
		t.Fatalf("deadline remaining = %s", remaining)
	}
}

func TestIdentityFromParamsTrimsFields(t *testing.T) {
	ident, ok := identityFromParams(libob.ResponseWriter{}, &libob.Request{
		Params: libob.EasierMapFromMap(map[string]interface{}{
			"platform":          " onebot ",
			"user_id":           " 42 ",
			"conversation_type": " group ",
			"conversation_id":   " 100 ",
		}),
	})
	if !ok {
		t.Fatal("identity was not parsed")
	}
	want := store.Identity{
		Platform:         "onebot",
		UserID:           "42",
		ConversationType: "group",
		ConversationID:   "100",
	}
	if ident != want {
		t.Fatalf("identity = %#v, want %#v", ident, want)
	}
}
