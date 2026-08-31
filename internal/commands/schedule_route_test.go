package commands

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestNaturalScheduleIntentRouting(t *testing.T) {
	tests := map[string]string{
		"帮我查一下今天的课表":      "today",
		"请看一下明天课表吧":       "tomorrow",
		"我下周有哪些课？":        "next-week",
		"麻烦查一下本周的课表":      "this-week",
		"想看看第3周课表":        "week-number:3",
		"查一下2026-05-06课表": "week-date:2026-05-06",
		"我想看2026秋季学期课表":   "semester:2026-秋",
	}
	handler := Handler{}
	for input, want := range tests {
		cmd, ok := handler.parse(input)
		if !ok || cmd.Name != "schedule" || joinedArgs(cmd.Args) != want || cmd.NaturalRoute != "schedule" {
			t.Errorf("%q parsed as %#v, ok=%v; want schedule %q", input, cmd, ok, want)
		}
	}
}

func TestNaturalScheduleIntentLeavesAmbiguousRequestsForAgent(t *testing.T) {
	inputs := []string{
		"为什么我的课表是空的",
		"查一下明天课表并且提醒我上课",
		"比较本周和下周课表",
		"明天有什么课以及作业",
		"帮我安排明天的学习计划",
		"课表怎么看",
		"我最近有点忙，看看怎么办",
	}
	handler := Handler{}
	for _, input := range inputs {
		if cmd, ok := handler.parse(input); ok {
			t.Errorf("ambiguous %q parsed as %#v", input, cmd)
		}
	}
}

func TestNaturalScheduleRouteIsHandledWithoutRawPromptLogging(t *testing.T) {
	var logs bytes.Buffer
	handler := Handler{Logger: log.New(&logs, "", 0)}
	response, ok := handler.HandleResponse(context.Background(), Input{
		Text: "帮我查一下今天的课表", Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
	})
	if !ok || !strings.Contains(response.Text, "Life @ USTC API unavailable") {
		t.Fatalf("response = %#v, ok=%v", response, ok)
	}
	if !strings.Contains(logs.String(), "route=schedule") || !strings.Contains(logs.String(), "latency_ms=") {
		t.Fatalf("routing metrics = %q", logs.String())
	}
	if strings.Contains(logs.String(), "帮我") || strings.Contains(logs.String(), "课表") {
		t.Fatalf("routing metrics contain raw prompt: %q", logs.String())
	}
}

func TestNaturalBusIntentRoutesDatesWithoutAgent(t *testing.T) {
	tests := map[string]string{
		"能查一下周六、周日的校车吗？":  "周六 周日",
		"帮我看明天东区到西区的校车":   "明天 东区 西区",
		"查一下2026-09-05校车": "2026-09-05",
	}
	for input, want := range tests {
		cmd, ok := (Handler{}).parse(input)
		if !ok || cmd.Name != "bus" || joinedArgs(cmd.Args) != want || cmd.NaturalRoute != "bus" {
			t.Errorf("%q parsed as %#v, ok=%v; want bus %q", input, cmd, ok, want)
		}
	}
}

func TestNaturalBusIntentLeavesDiagnosticQuestionsForAgent(t *testing.T) {
	for _, input := range []string{"为什么周六校车查不到", "周六校车查不到", "校车怎么设置偏好", "解释一下校车路线为什么变了"} {
		if cmd, ok := (Handler{}).parse(input); ok {
			t.Errorf("diagnostic %q parsed as %#v", input, cmd)
		}
	}
}
