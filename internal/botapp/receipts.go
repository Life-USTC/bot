package botapp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

const confirmationPrompt = "请确认标为「待确认」的这一项操作。回复 确认 执行，回复 取消 拒绝；其余操作会逐项询问。"

type executionReceipts struct {
	Lines []string
	IDs   []string
}

// formatExecutionReceipt renders the invocation stored in a durable execution.
// The execution result is deliberately kept out of the invocation itself: the
// host already sends the model-facing result, while this line records which
// command or tool was actually run and its terminal state.
func formatExecutionReceipt(execution store.CapabilityExecution) (string, bool) {
	status := capabilityExecutionActionReceiptStatus(execution.State)
	detail := capabilityExecutionActionReceiptDetail(execution)
	capability := strings.TrimSpace(execution.Capability)
	prefix, name, prefixed := strings.Cut(capability, ":")
	if prefixed && (strings.EqualFold(prefix, "mcp") || strings.EqualFold(prefix, "tool")) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", false
		}
		return formatMCPExecutionReceipt(name, execution.Arguments, status, detail)
	}
	command := botExecutionCommand(capability, execution.Arguments)
	return formatBotExecutionReceipt(command, status, detail)
}

func botExecutionCommand(capability string, arguments []string) string {
	capability = strings.TrimSpace(capability)
	invocation, ok := commands.NewInvocation(commands.CapabilityID(capability), arguments)
	if !ok {
		invocation, ok = commands.RestoreInvocation(commands.CapabilityID(capability), arguments)
	}
	if !ok {
		parts := make([]string, 0, len(arguments)+1)
		if capability != "" {
			parts = append(parts, capability)
		}
		for _, argument := range arguments {
			argument = strings.TrimSpace(argument)
			if strings.ContainsAny(argument, " \t\r\n\"") {
				argument = strconv.Quote(argument)
			}
			if argument != "" {
				parts = append(parts, argument)
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	parts := make([]string, 0, len(invocation.Args)+1)
	name := string(invocation.ID())
	if descriptor, found := commands.CapabilityDescriptorFor(invocation.ID()); found {
		for _, form := range descriptor.Forms {
			if strings.IndexFunc(form, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) >= 0 {
				name = form
				break
			}
		}
	}
	parts = append(parts, name)
	for _, argument := range invocation.Args {
		argument = strings.TrimSpace(argument)
		if strings.ContainsAny(argument, " \t\r\n\"") {
			argument = strconv.Quote(argument)
		}
		if argument != "" {
			parts = append(parts, argument)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func formatMCPExecutionReceipt(name string, arguments []string, status, detail string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	argumentText := formatMCPExecutionArguments(arguments)
	invocation := "<" + name + "()>"
	if argumentText != "" {
		invocation = "<" + name + "(" + argumentText + ")>"
	}
	return invocation + formatCapabilityExecutionActionReceiptStatus(status, detail), true
}

func formatMCPExecutionArguments(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}
	values := make([]any, len(arguments))
	for index, argument := range arguments {
		var value any
		decoder := json.NewDecoder(strings.NewReader(argument))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil || !json.Valid([]byte(argument)) {
			// Invalid arguments still identify the attempted invocation.
			values[index] = strings.TrimSpace(argument)
			continue
		}
		if index == 0 {
			if object, ok := value.(map[string]any); ok {
				// confirmed is a host-only authorization marker and is not an
				// argument supplied by the model.
				delete(object, "confirmed")
				if len(object) == 0 {
					value = nil
				}
			}
		}
		values[index] = redactReceiptArgument(value)
	}
	var value any = values
	if len(values) == 1 {
		if values[0] == nil {
			return ""
		}
		value = values[0]
	}
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return ""
	}
	return strings.TrimSuffix(encoded.String(), "\n")
}

func redactReceiptArgument(value any) any {
	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for key, nested := range value {
			if receiptSecretKey(key) {
				redacted[key] = "<redacted>"
				continue
			}
			redacted[key] = redactReceiptArgument(nested)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for index, nested := range value {
			redacted[index] = redactReceiptArgument(nested)
		}
		return redacted
	default:
		return value
	}
}

func receiptSecretKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "access_token", "refresh_token", "id_token", "token", "authorization",
		"api_key", "apikey", "client_secret", "secret", "password", "passwd",
		"cookie", "credential", "credentials", "device_code", "user_code":
		return true
	default:
		return strings.HasSuffix(normalized, "_token") || strings.HasSuffix(normalized, "_secret")
	}
}

func formatBotExecutionReceipt(command, status, detail string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	return "#" + command + formatCapabilityExecutionActionReceiptStatus(status, detail), true
}

func formatCapabilityExecutionActionReceiptStatus(status, detail string) string {
	label := strings.TrimSpace(status)
	if label == "" {
		label = "状态未知"
	}
	if strings.HasPrefix(label, "待确认") {
		if displayDetail := receiptDisplayDetail(detail); displayDetail != "" {
			label += "：" + displayDetail
		}
	}
	return "（" + label + "）"
}

func receiptDisplayDetail(detail string) string {
	return strings.TrimSpace(detail)
}

func capabilityExecutionActionReceiptStatus(state store.CapabilityExecutionState) string {
	switch state {
	case store.CapabilityExecutionSucceeded:
		return "已完成"
	case store.CapabilityExecutionFailed:
		return "失败"
	case store.CapabilityExecutionAwaitingConfirmation:
		return "待确认"
	case store.CapabilityExecutionDenied:
		return "已拒绝"
	case store.CapabilityExecutionUnknown:
		return "结果未知"
	case store.CapabilityExecutionCancelled:
		return "已取消"
	case store.CapabilityExecutionExpired:
		return "已过期"
	case store.CapabilityExecutionWaitingAuth:
		return "等待登录"
	case store.CapabilityExecutionApproved, store.CapabilityExecutionRunning:
		return "执行中"
	default:
		return "状态未知"
	}
}

func capabilityExecutionActionReceiptDetail(execution store.CapabilityExecution) string {
	switch execution.State {
	case store.CapabilityExecutionAwaitingConfirmation:
		// The descriptor-owned subject may contain the authoritative target
		// resolved during preflight. Keep it beside the real invocation so a
		// confirmation receipt cannot hide which item will be changed.
		return strings.TrimSpace(execution.Receipt.Subject)
	default:
		return ""
	}
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
		if !visible {
			continue
		}
		result.IDs = append(result.IDs, execution.ID)
		result.Lines = append(result.Lines, line)
	}
	return result, nil
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
		for _, item := range flattenResponseParts(response) {
			if strings.TrimSpace(item.Text) == "" && item.Image == nil {
				continue
			}
			parts = append(parts, item)
		}
	}
	if len(parts) == 0 {
		return commands.Response{}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return commands.Response{Parts: parts}
}
