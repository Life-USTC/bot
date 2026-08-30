package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestExecuteForAgentRunsReadOnlyHostCommand(t *testing.T) {
	result, err := (Handler{}).ExecuteForAgent(context.Background(), Input{
		Text: "帮助 校车",
		Identity: store.Identity{
			Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.Kind != "help" || !strings.Contains(result.Text, "校车") {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteForAgentPreparesMutationForRealUserConfirmation(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}

	result, err := (Handler{Store: db}).ExecuteForAgent(context.Background(), Input{
		Text: "通知 作业 开", Identity: ident,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "confirmation_required" || !result.ConfirmationRequired || result.Command != "notify homework on" {
		t.Fatalf("result = %#v", result)
	}
	pending, err := db.ActivePendingConfirmation(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Command != "notify homework on" || pending.Source != "agent" {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestExecuteForAgentCannotConfirmOnUsersBehalf(t *testing.T) {
	result, err := (Handler{}).ExecuteForAgent(context.Background(), Input{Text: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "forbidden" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteForAgentMarksPrivateCalendarLinkForHostDelivery(t *testing.T) {
	cmd, ok := (Handler{}).parse("订阅 链接")
	if !ok {
		t.Fatal("calendar link command did not parse")
	}
	if !agentCommandRequiresHostDelivery(cmd, Response{Text: "private"}) {
		t.Fatal("private calendar link must bypass the model")
	}
}

func TestExecuteForAgentKeepsLoginCredentialsOutOfModelResult(t *testing.T) {
	cmd, ok := (Handler{}).parse("登录")
	if !ok {
		t.Fatal("login command did not parse")
	}
	if !agentCommandRequiresHostDelivery(cmd, Response{Text: "验证码：SECRET"}) {
		t.Fatal("login response must bypass the model")
	}
}
