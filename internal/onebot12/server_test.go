package onebot12

import (
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
