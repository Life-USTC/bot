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

func TestExecuteCapabilityForAgentRunsReadOnlyHostCapability(t *testing.T) {
	result, err := (Handler{}).ExecuteCapabilityForAgent(context.Background(), Input{
		Identity: store.Identity{
			Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42",
		},
	}, CapabilityHelp, []string{"校车"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Status != AgentCommandStatusSuccess || result.Kind != "help" || !strings.Contains(result.Text, "校车") {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteCapabilityForAgentReturnsActionableInputStatuses(t *testing.T) {
	invalid, err := (Handler{}).ExecuteCapabilityForAgent(context.Background(), Input{}, CapabilityBus, []string{"not-a-day"})
	if err != nil {
		t.Fatal(err)
	}
	if invalid.OK || invalid.Status != AgentCommandStatusInvalidInput || len(invalid.SuggestedCalls) == 0 {
		t.Fatalf("invalid result = %#v", invalid)
	}
	for _, suggestion := range invalid.SuggestedCalls {
		if suggestion.Capability == "" || suggestion.Arguments == nil {
			t.Fatalf("unstructured suggestion = %#v", suggestion)
		}
	}

	missing, err := (Handler{}).ExecuteCapabilityForAgent(context.Background(), Input{}, CapabilityID("does_not_exist"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if missing.OK || missing.Status != AgentCommandStatusNotFound {
		t.Fatalf("missing result = %#v", missing)
	}
}

func TestExecuteCapabilityForAgentPreparesMutationForRealUserConfirmation(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}

	result, err := (Handler{Store: db}).ExecuteCapabilityForAgent(context.Background(), Input{Identity: ident}, CapabilityNotify, []string{"作业", "开"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Status != AgentCommandStatusConfirmationRequired || !result.ConfirmationRequired || result.Command != "notify homework on" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteCapabilityForAgentReturnsAuthRequiredWithoutExposingLoginData(t *testing.T) {
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
	authManager := &auth.Manager{Store: db}
	result, err := (Handler{
		Life:  life.NewClient("http://life.invalid", nil),
		Auth:  authManager,
		Store: db,
	}).ExecuteCapabilityForAgent(context.Background(), Input{Identity: ident}, CapabilitySchedule, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Status != AgentCommandStatusAuthRequired || !result.DeliveredByHost || result.Kind != ResponseKindAuthWait {
		t.Fatalf("auth result = %#v", result)
	}
	if strings.Contains(result.Text, userCode) || strings.Contains(result.Text, "login.example") {
		t.Fatalf("model-facing auth text = %q", result.Text)
	}
	if !strings.Contains(result.Response.Text, userCode) {
		t.Fatalf("host response = %#v", result.Response)
	}
}

func TestCapabilityPresentationMarksPrivateCalendarLinkForHostDelivery(t *testing.T) {
	cmd, ok := (Handler{}).parse("订阅 链接")
	if !ok {
		t.Fatal("calendar link command did not parse")
	}
	if !agentCommandRequiresHostDelivery(cmd, Response{Text: "private"}) {
		t.Fatal("private calendar link must bypass the model")
	}
}

func TestCapabilityPresentationKeepsLoginCredentialsOutOfModelResult(t *testing.T) {
	cmd, ok := (Handler{}).parse("登录")
	if !ok {
		t.Fatal("login command did not parse")
	}
	if !agentCommandRequiresHostDelivery(cmd, Response{Text: "验证码：SECRET"}) {
		t.Fatal("login response must bypass the model")
	}
	if !agentCommandRequiresHostDelivery(cmd, Response{Kind: ResponseKindAuthWait, Text: "验证码：SECRET"}) {
		t.Fatal("auth_wait response must bypass the model")
	}
}
