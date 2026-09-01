package botapp

import (
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestExecutionReceiptUsesOneFormatForPendingSuccessAndEveryFailure(t *testing.T) {
	receipt := store.CapabilityReceipt{Action: "订阅", Resource: "课程", Subject: "数学分析（程艺，2026年秋季学期）"}
	for _, test := range []struct {
		state  store.CapabilityExecutionState
		result string
		want   string
	}{
		{state: store.CapabilityExecutionAwaitingConfirmation, want: "#待确认订阅课程{数学分析（程艺，2026年秋季学期）}"},
		{state: store.CapabilityExecutionSucceeded, want: "#已订阅课程{数学分析（程艺，2026年秋季学期）}"},
		{state: store.CapabilityExecutionDenied, want: "#订阅课程失败{数学分析（程艺，2026年秋季学期）：用户拒绝执行}"},
		{state: store.CapabilityExecutionFailed, result: "课程不存在", want: "#订阅课程失败{数学分析（程艺，2026年秋季学期）：课程不存在}"},
		{state: store.CapabilityExecutionUnknown, want: "#订阅课程失败{数学分析（程艺，2026年秋季学期）：操作结果未知，系统没有自动重试}"},
		{state: store.CapabilityExecutionCancelled, want: "#订阅课程失败{数学分析（程艺，2026年秋季学期）：操作已取消}"},
		{state: store.CapabilityExecutionExpired, want: "#订阅课程失败{数学分析（程艺，2026年秋季学期）：操作已过期}"},
	} {
		got, visible := formatExecutionReceipt(store.CapabilityExecution{State: test.state, Receipt: receipt, Result: test.result})
		if !visible || got != test.want {
			t.Errorf("state %s receipt=%q visible=%v want=%q", test.state, got, visible, test.want)
		}
	}
}
