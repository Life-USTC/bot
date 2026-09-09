package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestParseCommandDistinguishesValidInvalidAndUnknown(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		status ParseStatus
		id     CapabilityID
	}{
		{name: "valid", input: "课表 明天", status: ParseStatusValid, id: CapabilitySchedule},
		{name: "invalid", input: "课表 someday", status: ParseStatusInvalid, id: CapabilitySchedule},
		{name: "invalid natural query", input: "帮我查询 someday 的课表", status: ParseStatusInvalid, id: CapabilitySchedule},
		{name: "casual schedule comment", input: "课表真难看", status: ParseStatusUnknown},
		{name: "unknown", input: "帮我规划一下周末", status: ParseStatusUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ParseCommand(test.input)
			if result.Status != test.status {
				t.Fatalf("ParseCommand(%q).Status = %q, want %q", test.input, result.Status, test.status)
			}
			if test.id != "" && result.Invocation.ID() != test.id {
				t.Fatalf("ParseCommand(%q).Invocation = %#v, want ID %q", test.input, result.Invocation, test.id)
			}
		})
	}
}

func TestParseCommandNormalizesChineseOrdinalWeek(t *testing.T) {
	result := ParseCommand("课表 第二周")
	if result.Status != ParseStatusValid || result.Invocation.ID() != CapabilitySchedule || strings.Join(result.Invocation.Args, " ") != "week-number:2" {
		t.Fatalf("ParseCommand Chinese week=%#v", result)
	}
}

func TestUnknownSlashCommandStaysUnrecognized(t *testing.T) {
	// A stray slash token must not become a public help invocation: that made a
	// shared conversation answer "/中午吃什么" without being addressed.
	for _, text := range []string{"/not-a-command ignored arguments", "/中午吃什么", "？", "?"} {
		if result := ParseCommand(text); result.Recognized() {
			t.Fatalf("%q should stay unrecognized: %#v", text, result)
		}
	}
	// A question mark still selects help as a sub-token of a real command.
	if result := ParseCommand("校车 ?"); result.Status != ParseStatusValid || result.Invocation.ID() != CapabilityHelp {
		t.Fatalf("bus help sub-token parse=%#v", result)
	}
	// Explicit help forms keep working.
	for _, text := range []string{"帮助", "help", "/help", "菜单"} {
		result := ParseCommand(text)
		if result.Status != ParseStatusValid || result.Invocation.ID() != CapabilityHelp {
			t.Fatalf("%q help parse=%#v", text, result)
		}
	}
}

func TestInvalidCommandReturnsUsageInsteadOfFallingThrough(t *testing.T) {
	for _, input := range []string{
		"课表 someday",
		"帮我查询 someday 的课表",
		"校车 火星",
		"校车 周六 nonsense",
		"通知 作业 开 nonsense",
		"设置 通知 课表 开 nonsense",
		"作业 all garbage",
		"作业 semester_id not-an-int",
		"作业 第2页 extra",
		"学期 列表 第2页",
		"日程 截止 第2页",
		"考试 第2页 extra",
		"订阅 导入",
		"订阅 导入 BAD",
		"订阅 导入 CONT5103P.01 nonsense",
		"课程 查看 not-an-id",
		"课程 搜索 数学 limit nope",
		"教学班 搜索 高等数学 学期ID nope",
		"老师 搜索 张 limit nope",
		"教学班 课表 12345 bad-date 2026-09-30",
		"作业 帮我看看还有啥",
	} {
		response, ok := (Handler{}).HandleResponse(context.Background(), Input{
			Text:     input,
			Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
		})
		if !ok || !strings.Contains(response.Text, "帮助：") {
			t.Fatalf("input = %q response = %#v, ok = %v; want capability usage", input, response, ok)
		}
	}
}

func TestNaturalRoutesUseDescriptorValidation(t *testing.T) {
	tests := []struct {
		input string
		id    CapabilityID
		args  string
		route string
	}{
		{input: "帮我查一下明天的课表", id: CapabilitySchedule, args: "tomorrow", route: "schedule"},
		{input: "帮我看周六校车", id: CapabilityBus, args: "周六", route: "bus"},
	}
	for _, test := range tests {
		result := ParseCommand(test.input)
		if result.Status != ParseStatusValid || result.Invocation.ID() != test.id || strings.Join(result.Invocation.Args, " ") != test.args || result.Invocation.NaturalRoute != test.route {
			t.Errorf("ParseCommand(%q) = %#v, want valid %q args=%q route=%q", test.input, result, test.id, test.args, test.route)
		}
	}
}

func TestUnknownNaturalLanguageRemainsUnknown(t *testing.T) {
	for _, input := range []string{
		"提醒我这周三之前我得把显微成像的数据集之类的研究清楚了",
		"反馈校车时间需要进一步整理",
		"建议你改进校车显示",
		"意见很大啊",
		"安排 一下明天行程",
		"我 明天有课吗",
		"工具 都有哪些",
		"个人 信息",
		"车 怎么坐去东区",
		"查一下火星校车",
		"帮我查校车",
		"push 一下",
		"p 通不通",
	} {
		result := ParseCommand(input)
		if result.Status != ParseStatusUnknown {
			t.Errorf("ParseCommand(%q) = %#v, want unknown", input, result)
		}
	}
}
