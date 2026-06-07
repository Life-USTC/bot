package onebot12

import (
	"strings"
	"testing"

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
