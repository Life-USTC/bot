package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestRecurringReminderResponseKeepsStructuredDataAndSelectableImage(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/reminders.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := Handler{Store: s}
	response, handled := h.HandleResponse(t.Context(), Input{Identity: testIdentity(), Text: "周期提醒 添加 每天 09:00 喝水"})
	if !handled || response.Image == nil || response.Image.Kind != "reminder" || !strings.Contains(response.Image.RichText, "喝水") {
		t.Fatalf("reminder presentation=%#v handled=%v", response, handled)
	}
	var result struct {
		Result struct {
			Operation string
			Reminder  map[string]any
		}
	}
	if err := json.Unmarshal([]byte(response.ModelResult("reminder", "success", time.Now())), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result.Operation != "recurring_reminder_add" || result.Result.Reminder["text"] != "喝水" || result.Result.Reminder["frequency"] != "daily" || result.Result.Reminder["timeOfDay"] != "09:00" || result.Result.Reminder["timezone"] != "Asia/Shanghai" || result.Result.Reminder["nextAt"] == nil {
		t.Fatalf("model result=%#v", result)
	}
}

func TestRecurringReminderHelpFormsExposeCommands(t *testing.T) {
	for _, command := range []string{"帮助 周期提醒", "周期提醒 帮助", "reminder help"} {
		t.Run(command, func(t *testing.T) {
			response, handled := (Handler{}).HandleResponse(t.Context(), Input{Identity: testIdentity(), Text: command})
			if !handled || response.Image == nil || response.Image.Kind != "help" || !strings.Contains(response.Image.RichText, "周期提醒 添加 每天 09:00") || !strings.Contains(response.Image.RichText, "周期提醒 暂停 1") {
				t.Fatalf("help=%#v handled=%v", response, handled)
			}
		})
	}
}

func TestRecurringReminderPreflightDescribesOwnedContent(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/reminders.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ident := testIdentity()
	rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, store.RecurringReminderSpec{Text: "写周报", Frequency: "weekly", Weekday: time.Monday, TimeOfDay: "09:00"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{Store: s}
	description, err := h.DescribeInvocation(t.Context(), Input{Identity: ident}, CapabilityReminder, []string{"delete", fmt.Sprint(rule.ID)})
	if err != nil || description.Receipt == nil || description.Receipt.Action != "删除" || !strings.Contains(description.Receipt.Subject, "写周报") || !strings.Contains(description.Receipt.Subject, fmt.Sprint(rule.ID)) {
		t.Fatalf("delete preflight=%#v err=%v", description, err)
	}
	other := ident
	other.UserID, other.ConversationID = "other", "other"
	if _, err := h.DescribeInvocation(t.Context(), Input{Identity: other}, CapabilityReminder, []string{"delete", fmt.Sprint(rule.ID)}); err == nil {
		t.Fatal("preflight exposed another owner's reminder")
	}
}

func TestRecurringReminderCommandPoliciesAndValidation(t *testing.T) {
	for _, tt := range []struct {
		command string
		effect  CapabilityEffect
	}{
		{"周期提醒", EffectRead},
		{"周期提醒 添加 每天 09:00 喝水", EffectWrite},
		{"周期提醒 添加 每周 周一 09:00 写周报 时区=America/New_York 截止=2099-12-31", EffectWrite},
		{"周期提醒 修改 1 每天 10:00 新内容", EffectWrite},
		{"周期提醒 暂停 1", EffectWrite},
		{"周期提醒 恢复 1", EffectWrite},
		{"周期提醒 删除 1", EffectDestructive},
	} {
		inv, ok := ParseInvocation(tt.command)
		if !ok || inv.ID() != CapabilityReminder || inv.Policy().Effect != tt.effect || inv.Policy().DataScope != DataScopeUserPrivate {
			t.Errorf("%s: %#v ok=%v", tt.command, inv, ok)
		}
	}
	for _, raw := range []string{"周期提醒 添加 每天 25:00 喝水", "周期提醒 添加 每周 周八 09:00 喝水", "周期提醒 添加 每天 09:00", "周期提醒 删除 0", "周期提醒 添加 每天 09:00 喝水 时区=未知"} {
		if _, ok := ParseInvocation(raw); ok {
			t.Errorf("accepted invalid reminder: %s", raw)
		}
	}
	if inv, ok := ParseInvocation("提醒 课表 开"); !ok || inv.ID() != CapabilityNotify {
		t.Fatal("existing notification alias changed")
	}
}

func TestRecurringReminderCommandsPreserveOptionsAndOwner(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/reminders.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := Handler{Store: s}
	ident := testIdentity()
	reply, handled := h.Handle(t.Context(), Input{Identity: ident, Text: "周期提醒 添加 每天 09:00 喝水 时区=America/New_York 截止=2099-12-31"})
	if !handled || !strings.Contains(reply, "喝水") {
		t.Fatalf("create reply=%q handled=%v", reply, handled)
	}
	rows, err := s.RecurringReminders(t.Context(), ident)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rules=%#v err=%v", rows, err)
	}
	id := rows[0].ID
	for _, command := range []string{fmt.Sprintf("周期提醒 修改 %d 每周 周二 10:00 修改内容", id), fmt.Sprintf("周期提醒 暂停 %d", id), fmt.Sprintf("周期提醒 恢复 %d", id)} {
		outcome, handled := h.Handle(t.Context(), Input{Identity: ident, Text: command})
		if !handled || !strings.Contains(outcome, "修改内容") {
			t.Fatalf("%s reply=%q handled=%v", command, outcome, handled)
		}
	}
	updated, err := s.RecurringReminder(t.Context(), ident, id)
	if err != nil || updated.Timezone != "America/New_York" || updated.EndDate != "2099-12-31" || updated.Paused || updated.NextAt == nil || !updated.NextAt.After(time.Now()) {
		t.Fatalf("edited=%#v err=%v", updated, err)
	}
	group := ident
	group.ConversationType, group.ConversationID = "group", "shared"
	result, err := h.ExecuteCapability(t.Context(), Input{Identity: group}, CapabilityReminder, []string{"list"})
	if err != nil || result.Status != CapabilityOutcomeForbidden || strings.Contains(result.Response.Text, "修改内容") {
		t.Fatalf("shared read=%#v err=%v", result, err)
	}
	bob := ident
	bob.UserID, bob.ConversationID = "other-user", "other-user"
	result, err = h.ExecuteCapability(t.Context(), Input{Identity: bob}, CapabilityReminder, []string{"delete", fmt.Sprint(id)})
	if err != nil || result.Status != CapabilityOutcomeNotFound {
		t.Fatalf("other owner delete=%#v err=%v", result, err)
	}
	reply, handled = h.Handle(t.Context(), Input{Identity: ident, Text: fmt.Sprintf("周期提醒 删除 %d", id)})
	if !handled || !strings.Contains(reply, "已删除") {
		t.Fatalf("delete=%q", reply)
	}
	rows, err = s.RecurringReminders(t.Context(), ident)
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted rules=%#v err=%v", rows, err)
	}
}

func TestNotificationOffPausesRecurringRulesAndOnDoesNotResumeThem(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/reminders.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := Handler{Store: s}
	ident := testIdentity()
	now := time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)
	rule, err := s.SaveRecurringReminder(t.Context(), ident, 0, store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, now)
	if err != nil {
		t.Fatal(err)
	}
	due := now.Add(90 * time.Second)
	if err := s.EnqueueDueRecurringReminders(t.Context(), due); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"通知 关", "通知 开"} {
		if reply, ok := h.Handle(t.Context(), Input{Identity: ident, Text: command}); !ok || !strings.Contains(reply, "提醒") {
			t.Fatalf("%s: %q", command, reply)
		}
		after, err := s.RecurringReminder(t.Context(), ident, rule.ID)
		if err != nil || !after.Paused || after.Text != "喝水" {
			t.Fatalf("%s changed rule unexpectedly: %#v err=%v", command, after, err)
		}
	}
	claimed, err := s.ClaimDue(t.Context(), due, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("globally disabled reminder remains deliverable: %#v err=%v", claimed, err)
	}
}
