package commands

import (
	"github.com/Life-USTC/Bot/internal/store"
	"strings"
	"testing"
)

func TestRainCommandsRequireExplicitOptInAndKeepCampus(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/rain.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := Handler{Store: s}
	ident := testIdentity()
	for _, tc := range []struct {
		command string
		enabled bool
		campus  string
	}{
		{"通知", false, "ustc-main"}, {"通知 开", false, "ustc-main"}, {"通知 降雨 开 高新", true, "ustc-gaoxin"}, {"通知 降雨 关", false, "ustc-gaoxin"}, {"通知 降雨 开 本部", true, "ustc-main"}, {"通知 关", false, "ustc-main"},
	} {
		reply, handled := h.Handle(t.Context(), Input{Text: tc.command, Identity: ident})
		if !handled || !strings.Contains(reply, "降雨提醒：") {
			t.Fatalf("%s reply=%s", tc.command, reply)
		}
		settings, err := s.RainSettings(t.Context(), ident)
		if err != nil || settings.Enabled != tc.enabled || settings.Campus != tc.campus {
			t.Fatalf("%s: %+v err=%v", tc.command, settings, err)
		}
	}
	for _, raw := range []string{"通知 降雨 开 月球", "通知 降雨 开 高新 本部", "通知 降雨 或许"} {
		if _, ok := h.parse(raw); ok {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
	group := ident
	group.ConversationType = "group"
	group.ConversationID = "group"
	reply, _ := h.Handle(t.Context(), Input{Text: "通知 降雨 开 高新", Identity: group})
	if !strings.Contains(reply, "私聊") {
		t.Fatal(reply)
	}
	settings, err := s.RainSettings(t.Context(), ident)
	if err != nil || settings.Enabled {
		t.Fatalf("group changed setting: %+v %v", settings, err)
	}
}
