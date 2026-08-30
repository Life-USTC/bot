package commands

import (
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestNaturalLanguageFallsThroughToAgent(t *testing.T) {
	handler := Handler{}
	shouldMiss := []string{
		"提醒我这周三之前我得把显微成像的数据集之类的研究清楚了",
		"反馈校车时间需要进一步整理",
		"建议你改进校车显示",
		"意见很大啊",
		"通知课表开",
		"提醒 我周三研究清楚",
		"安排 一下明天行程",
		"我 明天有课吗",
		"作业 帮我看看还有啥",
		"AI 帮我规划行程",
		"校车 我明天怎么走",
		"工具 都有哪些",
		"个人 信息",
		"车 怎么坐去东区",
		"push 一下",
		"p 通不通",
	}
	for _, text := range shouldMiss {
		if cmd, ok := handler.parse(text); ok {
			t.Fatalf("%q unexpectedly parsed as %#v", text, cmd)
		}
	}
}

func TestFixedPatternsStillParse(t *testing.T) {
	handler := Handler{}
	tests := map[string]string{
		"提醒 课表 开":      "notify",
		"通知 作业 关":      "notify",
		"设置 通知 课表 开":   "notify",
		"反馈 校车时间需要整理":  "feedback",
		"今日课表":         "schedule",
		"明日课表":         "schedule",
		"课表 今天":        "schedule",
		"今天 课表":        "schedule",
		"待办":           "todo",
		"td 写报告":       "todo",
		"作业":           "homework",
		"作业 done 1":    "homework",
		"校车":           "bus",
		"xc 东区 西区":     "bus",
		"课程 数学分析":      "course",
		"老师 程艺":        "teacher",
		"AI 工具 开":      "agent",
		"给我26秋课表的订阅链接": "subscription",
		"请给我日历订阅链接":    "subscription",
		"帮助":           "help",
		"/help":        "help",
	}
	for text, want := range tests {
		cmd, ok := handler.parse(text)
		if !ok || cmd.Name != want {
			t.Fatalf("%q parsed as %#v ok=%v, want %q", text, cmd, ok, want)
		}
	}
}

func TestNaturalCalendarLinkRequestUsesHostCommand(t *testing.T) {
	cmd, ok := (Handler{}).parse("给我26秋课表的日历订阅链接")
	if !ok || cmd.Name != "subscription" || len(cmd.Args) != 1 || cmd.Args[0] != "link" {
		t.Fatalf("parsed as %#v, ok=%v", cmd, ok)
	}
}

func TestGroupAtHelpParsesAfterCQStrip(t *testing.T) {
	handler := Handler{}
	group := store.Identity{Platform: "napcat", UserID: "1", ConversationType: "group", ConversationID: "9"}

	reply, ok := handler.Handle(t.Context(), Input{
		Text:         "[CQ:at,qq=3889719924] /help",
		Identity:     group,
		BotMentioned: true,
	})
	if !ok || reply == "" {
		t.Fatalf("group @help reply=%q ok=%v", reply, ok)
	}

	reply, ok = handler.Handle(t.Context(), Input{
		Text:     "课表",
		Identity: group,
	})
	if ok || reply != "" {
		t.Fatalf("group 课表 without @ should be silent, reply=%q ok=%v", reply, ok)
	}

	reply, ok = handler.Handle(t.Context(), Input{
		Text:         "[CQ:at,qq=3889719924] 课表",
		Identity:     group,
		BotMentioned: true,
	})
	if !ok || reply != "此功能请私聊使用。" {
		t.Fatalf("group @课表 reply=%q ok=%v", reply, ok)
	}

	reply, ok = handler.Handle(t.Context(), Input{
		Text:         "[CQ:at,qq=3889719924] 课表 help",
		Identity:     group,
		BotMentioned: true,
	})
	if !ok || reply != "此功能请私聊使用。" {
		t.Fatalf("group @课表 help reply=%q ok=%v", reply, ok)
	}
}
