package routing

import (
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
)

func groupMessage(text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: "napcat", UserID: "42"},
		Conversation: message.Conversation{Platform: "napcat", Type: "group", ID: "100"},
		Text:         text,
	}
}

func TestSharedConversationActivationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		mentioned  bool
		wantAction Action
		wantID     commands.CapabilityID
	}{
		{name: "exact public command", text: "校车", wantAction: ActionCommand, wantID: commands.CapabilityBus},
		{name: "bare room code", text: "5201", wantAction: ActionIgnore},
		{name: "bare fullwidth room code", text: "ｇｔ－ｂ１１０", wantAction: ActionIgnore},
		{name: "room announcement", text: "明天在5201上课", wantAction: ActionIgnore},
		{name: "ordinary number", text: "2026", wantAction: ActionIgnore},
		{name: "room map command", text: "教室 3A204", wantAction: ActionCommand, wantID: commands.CapabilityRoomMap},
		{name: "room map natural query", text: "3A204 在哪里？", wantAction: ActionIgnore},
		{name: "public command with route", text: "校车 西区 高新区", wantAction: ActionCommand, wantID: commands.CapabilityBus},
		{name: "strict public natural query", text: "查一下周六西区到高新区的校车", wantAction: ActionIgnore},
		{name: "addressed public natural query", text: "查一下周六西区到高新区的校车", mentioned: true, wantAction: ActionCommand, wantID: commands.CapabilityBus},
		{name: "addressed room lookup", text: "3A204 在哪里？", mentioned: true, wantAction: ActionCommand, wantID: commands.CapabilityRoomMap},
		{name: "ambient opinion", text: "今天校车好挤", wantAction: ActionIgnore},
		{name: "ambient plan", text: "大家周六坐校车去聚餐", wantAction: ActionIgnore},
		{name: "announcement containing query word", text: "周六校车还有调整通知", wantAction: ActionIgnore},
		{name: "unknown ambient text", text: "晚上一起吃饭吗", wantAction: ActionIgnore},
		{name: "addressed unknown request", text: "这个安排合理吗", mentioned: true, wantAction: ActionAgent},
		{name: "exact private command redirects", text: "课表", wantAction: ActionIgnore},
		{name: "addressed private command", text: "课表", mentioned: true, wantAction: ActionCommand, wantID: commands.CapabilitySchedule},
		{name: "unaddressed private natural request", text: "帮我查一下明天的课表", wantAction: ActionIgnore},
		{name: "addressed private natural request", text: "帮我查一下明天的课表", mentioned: true, wantAction: ActionCommand, wantID: commands.CapabilitySchedule},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inbound := groupMessage(test.text)
			inbound.BotMentioned = test.mentioned
			decision := Decide(inbound, nil)
			if decision.Action != test.wantAction || decision.Invocation.ID() != test.wantID {
				t.Fatalf("Decide(%q) = %#v, want action=%q id=%q", test.text, decision, test.wantAction, test.wantID)
			}
		})
	}
}

func TestPublicBusReplyReplacesDateAndKeepsRoute(t *testing.T) {
	inbound := groupMessage("周日呢")
	context := &message.ResponseContext{
		Capability: string(commands.CapabilityBus),
		Arguments:  []string{"周六", "西区", "高新区"},
	}
	decision := Decide(inbound, context)
	if decision.Action != ActionCommand || decision.Activation != ActivationReply || decision.Invocation.ID() != commands.CapabilityBus {
		t.Fatalf("decision = %#v", decision)
	}
	got := strings.Join(decision.Invocation.Args, " ")
	if got != "西区 高新区 周日" {
		t.Fatalf("follow-up args = %q", got)
	}
}

func TestVerifiedReplyToAgentOutputActivatesAgent(t *testing.T) {
	inbound := groupMessage("这个结果是什么意思")
	decision := Decide(inbound, &message.ResponseContext{})
	if decision.Action != ActionAgent || decision.Activation != ActivationReply {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSharedBareHelpAndUnknownSlashAreIgnoredUnlessAddressed(t *testing.T) {
	for _, text := range []string{"?", "？", "/unknown"} {
		t.Run(text, func(t *testing.T) {
			inbound := groupMessage(text)
			if decision := Decide(inbound, nil); decision.Action != ActionIgnore {
				t.Fatalf("unaddressed %q = %#v", text, decision)
			}
			inbound.BotMentioned = true
			if decision := Decide(inbound, nil); decision.Action != ActionAgent {
				t.Fatalf("addressed %q = %#v", text, decision)
			}
		})
	}
}

func TestSharedFuzzyPublicLookupNeedsAddressing(t *testing.T) {
	for _, text := range []string{"5201", "3A204 在哪里？", "查一下周六西区到高新区的校车"} {
		inbound := groupMessage(text)
		if decision := Decide(inbound, nil); decision.Action != ActionIgnore {
			t.Fatalf("unaddressed fuzzy lookup %q = %#v", text, decision)
		}
		inbound.BotMentioned = true
		if decision := Decide(inbound, nil); decision.Action != ActionCommand {
			t.Fatalf("addressed fuzzy lookup %q = %#v", text, decision)
		}
	}
}

func TestConversationSurfaceDrivesOneRouter(t *testing.T) {
	channel := groupMessage("校车 西区 高新区")
	channel.Conversation.Type = "channel"
	if decision := Decide(channel, nil); decision.Action != ActionCommand {
		t.Fatalf("channel decision = %#v", decision)
	}

	direct := groupMessage("帮我规划明天")
	direct.Conversation.Type = "guild_private"
	if decision := Decide(direct, nil); decision.Action != ActionAgent || decision.Activation != ActivationDirect {
		t.Fatalf("guild direct decision = %#v", decision)
	}
}

func TestRemovedLifePrefixNeverFallsThroughToAgentOrNaturalRoutes(t *testing.T) {
	for _, inbound := range []message.Inbound{
		{
			Actor:        message.Actor{Platform: "napcat", UserID: "42"},
			Conversation: message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
			Text:         "/life课表订阅链接",
		},
		func() message.Inbound {
			inbound := groupMessage("/life 帮我查课表")
			inbound.BotMentioned = true
			return inbound
		}(),
	} {
		if decision := Decide(inbound, nil); decision.Action != ActionIgnore {
			t.Fatalf("removed prefix routed as %#v", decision)
		}
	}
}

func TestMediaOnlyMessagesRespectConversationActivation(t *testing.T) {
	inbound := message.Inbound{Conversation: message.Conversation{Type: "private"}, Media: []message.InputMedia{{Kind: message.InputMediaFile, Name: "report.pdf"}}}
	if got := Decide(inbound, nil); got.Action != ActionAgent {
		t.Fatalf("private file decision=%#v", got)
	}
	inbound.Conversation.Type = "group"
	if got := Decide(inbound, nil); got.Action != ActionIgnore {
		t.Fatalf("ambient group file activated agent=%#v", got)
	}
	inbound.BotMentioned = true
	if got := Decide(inbound, nil); got.Action != ActionAgent {
		t.Fatalf("addressed group file decision=%#v", got)
	}
}
