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

func TestInvalidCommandReturnsUsageInsteadOfFallingThrough(t *testing.T) {
	response, ok := (Handler{}).HandleResponse(context.Background(), Input{
		Text:     "课表 someday",
		Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
	})
	if !ok || !strings.Contains(response.Text, "课表 帮助：") {
		t.Fatalf("response = %#v, ok = %v; want schedule usage", response, ok)
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
	} {
		result := ParseCommand(input)
		if result.Status != ParseStatusUnknown {
			t.Errorf("ParseCommand(%q) = %#v, want unknown", input, result)
		}
	}
}
