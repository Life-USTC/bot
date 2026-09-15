package botapp

import (
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestExecutionReceiptFormatsBotReadAndMutationInvocation(t *testing.T) {
	read, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "bus", Arguments: []string{"西区", "高新区"}, State: store.CapabilityExecutionSucceeded,
	})
	if !visible || read != "#校车 西区 高新区（已完成）" {
		t.Fatalf("Bot read receipt=%q visible=%v", read, visible)
	}

	pending, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "subscription", Arguments: []string{"import", "MATH1006.01"},
		State:   store.CapabilityExecutionAwaitingConfirmation,
		Receipt: store.CapabilityReceipt{Subject: "数学分析（程艺，2026年秋季学期）"},
	})
	wantPending := "#订阅 import MATH1006.01（待确认：数学分析（程艺，2026年秋季学期））"
	if !visible || pending != wantPending {
		t.Fatalf("Bot pending receipt=%q visible=%v want=%q", pending, visible, wantPending)
	}

	withSpaces, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "todo", Arguments: []string{"add", "写 实验 报告"}, State: store.CapabilityExecutionSucceeded,
	})
	if !visible || withSpaces != `#待办 add "写 实验 报告"（已完成）` {
		t.Fatalf("Bot argument receipt=%q visible=%v", withSpaces, visible)
	}
}

func TestExecutionReceiptFormatsMCPAndAuxiliaryToolInvocations(t *testing.T) {
	read, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "mcp:catalog_rooms_map", Arguments: []string{`{"campus":"高新区","confirmed":false}`}, State: store.CapabilityExecutionSucceeded,
	})
	if !visible || read != `<catalog_rooms_map({"campus":"高新区"})>（已完成）` {
		t.Fatalf("MCP read receipt=%q visible=%v", read, visible)
	}

	tool, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "tool:get_current_time", Arguments: []string{`{"timezone":"Asia/Shanghai"}`}, State: store.CapabilityExecutionSucceeded,
	})
	if !visible || tool != `<get_current_time({"timezone":"Asia/Shanghai"})>（已完成）` {
		t.Fatalf("auxiliary tool receipt=%q visible=%v", tool, visible)
	}

	withCredentials, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "mcp:catalog_rooms_map",
		Arguments: []string{`{"url":"https://life.example/rooms","access_token":"private-value","nested":{"authorization":"Bearer private-token"}}`},
		State: store.CapabilityExecutionSucceeded,
	})
	wantCredentials := `<catalog_rooms_map({"access_token":"<redacted>","nested":{"authorization":"<redacted>"},"url":"https://life.example/rooms"})>（已完成）`
	if !visible || withCredentials != wantCredentials {
		t.Fatalf("MCP argument receipt=%q visible=%v want=%q", withCredentials, visible, wantCredentials)
	}

	mutation, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "mcp:update_todo", Arguments: []string{`{"title":"整理实验数据"}`}, State: store.CapabilityExecutionDenied,
	})
	if !visible || mutation != `<update_todo({"title":"整理实验数据"})>（已拒绝）` {
		t.Fatalf("MCP mutation receipt=%q visible=%v", mutation, visible)
	}
}

func TestExecutionReceiptReportsErrorsWithoutRawResultOrCredentials(t *testing.T) {
	failure, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "mcp:update_todo", Arguments: []string{`{"title":"整理实验数据"}`}, State: store.CapabilityExecutionFailed,
		Error: `request failed: https://example.invalid token=secret-token`, Result: `{"secret":"business-result"}`,
	})
	if !visible || failure != `<update_todo({"title":"整理实验数据"})>（失败）` {
		t.Fatalf("MCP failed receipt=%q visible=%v", failure, visible)
	}
	for _, leaked := range []string{"business-result", "example.invalid", "secret-token", `{"secret"`} {
		if strings.Contains(failure, leaked) {
			t.Fatalf("MCP receipt leaked %q: %q", leaked, failure)
		}
	}

	unknown, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "bus", Arguments: []string{"东区", "西区"}, State: store.CapabilityExecutionUnknown,
	})
	if !visible || unknown != "#校车 东区 西区（结果未知）" {
		t.Fatalf("unknown receipt=%q visible=%v", unknown, visible)
	}
}

func TestExecutionReceiptKeepsLongConfirmationTarget(t *testing.T) {
	target := strings.Repeat("课程目标", 300) + "-target-at-end"
	receipt, visible := formatExecutionReceipt(store.CapabilityExecution{
		Capability: "todo", Arguments: []string{"delete", target}, State: store.CapabilityExecutionAwaitingConfirmation,
		Receipt: store.CapabilityReceipt{Subject: target},
	})
	if !visible || len([]rune(receipt)) <= 1000 || !strings.HasSuffix(receipt, target+"）") {
		t.Fatalf("long confirmation receipt length=%d suffix=%v", len([]rune(receipt)), strings.HasSuffix(receipt, target+"）"))
	}
}

func TestAppendReceiptLinesKeepsModelResponseAndReceiptSeparate(t *testing.T) {
	response := appendReceiptLines(
		commands.Response{Text: "模型结果", Data: map[string]any{"answer": "private"}},
		"",
		executionReceipts{Lines: []string{`<update_todo({"title":"整理实验数据"})>（已完成）`}},
	)
	if response.Text != "模型结果\n\n<update_todo({\"title\":\"整理实验数据\"})>（已完成）" {
		t.Fatalf("response text=%q", response.Text)
	}
	if response.Data == nil || response.Kind != "agent_receipt" {
		t.Fatalf("response metadata=%#v", response)
	}
}
