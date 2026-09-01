package botapp

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

const confirmationPrompt = "请确认是否执行以下操作。回复 ok 确认，回复 取消 拒绝。"

type executionReceipts struct {
	Lines []string
	IDs   []string
}

func (c *Coordinator) unsentExecutionReceipts(ctx context.Context, jobID int64, includeOnePending bool) (executionReceipts, error) {
	executions, err := c.jobs.UnsentCapabilityExecutionsForJob(ctx, jobID)
	if err != nil {
		return executionReceipts{}, err
	}
	result := executionReceipts{}
	seen := make(map[string]bool)
	pendingIncluded := false
	for _, execution := range executions {
		if strings.TrimSpace(execution.Receipt.Action) == "" || strings.TrimSpace(execution.Receipt.Resource) == "" {
			continue
		}
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			if !includeOnePending || pendingIncluded {
				continue
			}
			pendingIncluded = true
		}
		line, visible := formatExecutionReceipt(execution)
		if !visible {
			continue
		}
		result.IDs = append(result.IDs, execution.ID)
		if seen[line] {
			continue
		}
		seen[line] = true
		result.Lines = append(result.Lines, line)
	}
	return result, nil
}

func formatExecutionReceipt(execution store.CapabilityExecution) (string, bool) {
	action := strings.TrimSpace(execution.Receipt.Action)
	resource := strings.TrimSpace(execution.Receipt.Resource)
	subject := strings.TrimSpace(execution.Receipt.Subject)
	if action == "" || resource == "" || subject == "" {
		return "", false
	}
	switch execution.State {
	case store.CapabilityExecutionAwaitingConfirmation:
		return "#待确认" + action + resource + "{" + subject + "}", true
	case store.CapabilityExecutionSucceeded:
		return "#已" + action + resource + "{" + subject + "}", true
	case store.CapabilityExecutionDenied, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
		reason := receiptFailureReason(execution)
		return "#" + action + resource + "失败{" + subject + "：" + reason + "}", true
	default:
		return "", false
	}
}

func receiptFailureReason(execution store.CapabilityExecution) string {
	reason := strings.TrimSpace(execution.Error)
	if reason == "" {
		switch execution.State {
		case store.CapabilityExecutionDenied:
			reason = "用户拒绝执行"
		case store.CapabilityExecutionUnknown:
			reason = "操作结果未知，系统没有自动重试"
		default:
			reason = "操作没有完成"
		}
	}
	const maxRunes = 160
	if utf8.RuneCountInString(reason) > maxRunes {
		reason = string([]rune(reason)[:maxRunes]) + "…"
	}
	return reason
}

func appendReceiptLines(response commands.Response, prefix string, receipts executionReceipts) commands.Response {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(response.Text); text != "" {
		parts = append(parts, text)
	}
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		parts = append(parts, prefix)
	}
	if len(receipts.Lines) > 0 {
		parts = append(parts, strings.Join(receipts.Lines, "\n"))
	}
	response.Text = strings.Join(parts, "\n\n")
	if response.Kind == "" {
		response.Kind = "agent_receipt"
	}
	return response
}
