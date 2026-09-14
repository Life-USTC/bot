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
	return c.unsentExecutionReceiptsForEffect(ctx, jobID, includeOnePending, "")
}

func (c *Coordinator) unsentExecutionReceiptsForEffect(ctx context.Context, jobID int64, includeOnePending bool, effect string) (executionReceipts, error) {
	executions, err := c.jobs.UnsentCapabilityExecutionsForJob(ctx, jobID)
	if err != nil {
		return executionReceipts{}, markConversationPersistenceError(err)
	}
	result := executionReceipts{}
	seen := make(map[string]bool)
	pendingIncluded := false
	for _, execution := range executions {
		if effect != "" && !strings.EqualFold(strings.TrimSpace(execution.Effect), strings.TrimSpace(effect)) {
			continue
		}
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			if !includeOnePending || pendingIncluded {
				continue
			}
			pendingIncluded = true
		}
		line, visible := formatExecutionReceipt(execution)
		if !visible && execution.State == store.CapabilityExecutionAwaitingConfirmation {
			line, visible = formatUnlabeledExecutionReceipt(execution)
		}
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

func formatUnlabeledExecutionReceipt(execution store.CapabilityExecution) (string, bool) {
	if execution.State != store.CapabilityExecutionAwaitingConfirmation || strings.TrimSpace(execution.Capability) == "" {
		return "", false
	}
	subject := strings.TrimSpace(strings.Join(execution.Arguments, " "))
	if subject == "" {
		subject = "无参数"
	}
	return "#待确认操作{" + execution.Capability + " " + subject + "}", true
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
	case store.CapabilityExecutionDenied, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown,
		store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
		reason := receiptFailureReason(execution)
		return "#" + action + resource + "失败{" + subject + "：" + reason + "}", true
	default:
		return "", false
	}
}

func receiptFailureReason(execution store.CapabilityExecution) string {
	var reason string
	switch execution.State {
	case store.CapabilityExecutionDenied:
		reason = "用户拒绝执行"
	case store.CapabilityExecutionUnknown:
		reason = "操作结果未知，系统没有自动重试"
	case store.CapabilityExecutionFailed:
		// Result is the descriptor-owned, user-safe domain response. Error may
		// contain protected transport diagnostics when execution failed before
		// the descriptor could return a result.
		reason = capabilityExecutionResultText(execution)
		if reason == "" {
			reason = "操作没有完成"
		}
	case store.CapabilityExecutionCancelled:
		reason = "操作已取消"
	case store.CapabilityExecutionExpired:
		reason = "操作已过期"
	default:
		reason = "操作没有完成"
	}
	const maxRunes = 160
	if utf8.RuneCountInString(reason) > maxRunes {
		reason = string([]rune(reason)[:maxRunes]) + "…"
	}
	return reason
}

func appendReceiptLines(response commands.Response, prefix string, receipts executionReceipts) commands.Response {
	parts := make([]string, 0, 2)
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		parts = append(parts, prefix)
	}
	if len(receipts.Lines) > 0 {
		parts = append(parts, strings.Join(receipts.Lines, "\n"))
	}
	receiptText := strings.Join(parts, "\n\n")
	if len(response.Parts) > 0 {
		if receiptText != "" {
			response.Parts = append(response.Parts, commands.Response{Text: receiptText, Kind: "agent_receipt"})
		}
		return response
	}
	if response.Image != nil && receiptText != "" {
		return commands.Response{Parts: []commands.Response{
			response,
			{Text: receiptText, Kind: "agent_receipt"},
		}}
	}
	if text := strings.TrimSpace(response.Text); text != "" {
		if receiptText != "" {
			response.Text = text + "\n\n" + receiptText
		} else {
			response.Text = text
		}
	} else {
		response.Text = receiptText
	}
	if response.Kind == "" {
		response.Kind = "agent_receipt"
	}
	return response
}

func combineResponses(responses ...commands.Response) commands.Response {
	parts := make([]commands.Response, 0, len(responses))
	for _, response := range responses {
		if len(response.Parts) > 0 {
			parts = append(parts, response.Parts...)
			continue
		}
		if strings.TrimSpace(response.Text) == "" && response.Image == nil {
			continue
		}
		parts = append(parts, response)
	}
	if len(parts) == 0 {
		return commands.Response{}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return commands.Response{Parts: parts}
}
