package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestCapabilityExecutionReturnsTypedStateAndLiteralPresentation(t *testing.T) {
	handler := Handler{}
	outcome, err := handler.ExecuteCapability(context.Background(), Input{
		Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
	}, CapabilityHelp, []string{"校车"})
	if err != nil || outcome.Status != CapabilityOutcomeSuccess || !strings.Contains(outcome.Response.Text, "校车") {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	invocation, _ := NewInvocation(CapabilityHelp, []string{"校车"})
	presentation := handler.PresentCapabilityOutcome(invocation, outcome)
	if presentation.DeliveredByHost || presentation.Text != outcome.Response.Text {
		t.Fatalf("presentation=%#v outcome=%#v", presentation, outcome)
	}
}

func TestCapabilityExecutionEnforcesSharedDataScope(t *testing.T) {
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "100"}
	public, err := (Handler{}).ExecuteCapability(t.Context(), Input{Identity: ident}, CapabilityHelp, []string{"校车"})
	if err != nil || public.Status != CapabilityOutcomeSuccess {
		t.Fatalf("public=%#v err=%v", public, err)
	}
	for _, test := range []struct {
		id   CapabilityID
		args []string
	}{{id: CapabilitySchedule}, {id: CapabilityBus, args: []string{"偏好"}}} {
		outcome, err := (Handler{}).ExecuteCapability(t.Context(), Input{Identity: ident}, test.id, test.args)
		if err != nil || outcome.Status != CapabilityOutcomeForbidden {
			t.Fatalf("private %s outcome=%#v err=%v", test.id, outcome, err)
		}
	}
}

func TestCapabilityExecutionReturnsExplicitInvalidAndMissingStates(t *testing.T) {
	invalid, err := (Handler{}).ExecuteCapability(context.Background(), Input{}, CapabilityBus, []string{"not-a-day"})
	if err != nil || invalid.Status != CapabilityOutcomeInvalidInput ||
		!strings.Contains(invalid.Response.Text, "校车的参数无法识别") ||
		!strings.Contains(invalid.Response.Text, "校车 周六 周日") ||
		!strings.Contains(invalid.Response.Text, "校车 偏好 路线 东区 西区") ||
		strings.Contains(invalid.Response.Text, "命令文档") {
		t.Fatalf("invalid=%#v err=%v", invalid, err)
	}
	schedule, err := (Handler{}).ExecuteCapability(context.Background(), Input{}, CapabilitySchedule, []string{"someday"})
	if err != nil || schedule.Status != CapabilityOutcomeInvalidInput || !strings.Contains(schedule.Response.Text, "课表 第3周") {
		t.Fatalf("schedule invalid=%#v err=%v", schedule, err)
	}
	missing, err := (Handler{}).ExecuteCapability(context.Background(), Input{}, CapabilityID("does_not_exist"), nil)
	if err != nil || missing.Status != CapabilityOutcomeNotFound {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}
}

func TestCapabilityExecutionLeavesConfirmationToHost(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	outcome, err := (Handler{Store: db}).ExecuteCapability(context.Background(), Input{Identity: ident}, CapabilityNotify, []string{"作业", "开"})
	if err != nil || outcome.Status != CapabilityOutcomeSuccess || !outcome.ConfirmationRequired || outcome.Response.Text != "" {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
}

func TestCapabilityPresentationKeepsLoginCredentialsHostOnly(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	const userCode = "ABCD-SECRET"
	if err := db.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", UserCode: userCode, VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	handler := Handler{Life: life.NewClient("http://life.invalid", nil), Auth: &auth.Manager{Store: db}, Store: db}
	outcome, err := handler.ExecuteCapability(context.Background(), Input{Identity: ident}, CapabilitySchedule, nil)
	if err != nil || outcome.Status != CapabilityOutcomeAuthRequired || !strings.Contains(outcome.Response.Text, userCode) {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	invocation, _ := NewInvocation(CapabilitySchedule, nil)
	presentation := handler.PresentCapabilityOutcome(invocation, outcome)
	if !presentation.DeliveredByHost || strings.Contains(presentation.Text, userCode) || strings.Contains(presentation.Text, "login.example") {
		t.Fatalf("presentation=%#v", presentation)
	}
}

func TestCapabilityPresentationExposesPrivateCalendarURLToModel(t *testing.T) {
	invocation, ok := ParseInvocation("订阅 链接")
	if !ok {
		t.Fatal("calendar link command did not parse")
	}
	const result = "日历订阅链接：https://life.example/api/calendar-feeds/user:token.ics"
	presentation := (Handler{}).PresentCapabilityOutcome(invocation, SuccessOutcome(Response{Text: result, Kind: "subscription"}))
	if presentation.DeliveredByHost || presentation.Text != result {
		t.Fatalf("presentation=%#v", presentation)
	}
}
