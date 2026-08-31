package commands

import (
	"context"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	AgentCommandStatusSuccess              = "success"
	AgentCommandStatusInvalidInput         = "invalid_input"
	AgentCommandStatusForbidden            = "forbidden"
	AgentCommandStatusConfirmationRequired = "confirmation_required"
	AgentCommandStatusAuthRequired         = "auth_required"
	AgentCommandStatusNotFound             = "not_found"
)

// AgentCommandResult is the host-authoritative result of an Agent-requested
// command. Response is retained for application-layer delivery of images and
// private values; it is deliberately excluded from model-facing JSON.
type AgentCommandResult struct {
	OK                   bool                     `json:"ok"`
	Status               string                   `json:"status"`
	Command              string                   `json:"command,omitempty"`
	Kind                 string                   `json:"kind,omitempty"`
	Text                 string                   `json:"text,omitempty"`
	SuggestedCalls       []CapabilityUsageExample `json:"suggestedCalls,omitempty"`
	ConfirmationRequired bool                     `json:"confirmationRequired,omitempty"`
	DeliveredByHost      bool                     `json:"deliveredByHost,omitempty"`
	Response             Response                 `json:"-"`
}

// ExecuteCapabilityForAgent executes one validated structured capability.
// This is the Agent boundary; command strings remain a user-facing syntax.
func (h Handler) ExecuteCapabilityForAgent(ctx context.Context, input Input, id CapabilityID, args []string) (AgentCommandResult, error) {
	_, found := CapabilityDescriptorFor(id)
	if !found {
		return AgentCommandResult{
			Status:  AgentCommandStatusNotFound,
			Command: string(id),
			Kind:    string(id),
			Text:    "没有找到这个能力。请使用工具描述中的稳定 capability ID。",
		}, nil
	}
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return AgentCommandResult{
			Status:         AgentCommandStatusInvalidInput,
			Kind:           string(id),
			Text:           "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。",
			SuggestedCalls: CapabilityUsageExamples(id),
		}, nil
	}
	return h.executeInvocationForAgent(ctx, input, invocation)
}

func (h Handler) executeInvocationForAgent(ctx context.Context, input Input, invocation Invocation) (AgentCommandResult, error) {
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return AgentCommandResult{
			OK: false, Status: AgentCommandStatusForbidden, Command: canonicalAgentCommand(invocation),
			Kind: invocation.Name, Text: "此功能只能在私聊使用。",
		}, nil
	}
	command := canonicalAgentCommand(invocation)
	policy := invocation.Capability.PolicyFor(invocation)
	if policy.Confirmation == ConfirmUser {
		return AgentCommandResult{
			OK:                   false,
			Status:               AgentCommandStatusConfirmationRequired,
			Command:              command,
			Kind:                 invocation.Name,
			Text:                 "需要确认：" + command + "\n回复 ok 后执行。",
			ConfirmationRequired: true,
		}, nil
	}

	response, handled := h.executeInvocation(ctx, input, invocation)
	if !handled {
		return AgentCommandResult{Status: AgentCommandStatusNotFound, Command: command, Kind: invocation.Name, Text: "宿主无法执行这条命令。"}, nil
	}
	if response.Kind == ResponseKindAuthWait {
		return AgentCommandResult{
			OK:              false,
			Status:          AgentCommandStatusAuthRequired,
			Command:         command,
			Kind:            response.Kind,
			Text:            "需要登录；登录提示已由宿主安全发送，授权后会继续刚才的请求。",
			DeliveredByHost: true,
			Response:        response,
		}, nil
	}
	presentation := invocation.Capability.Present(invocation, response, policy)
	return AgentCommandResult{
		OK:              true,
		Status:          AgentCommandStatusSuccess,
		Command:         command,
		Kind:            response.Kind,
		Text:            presentation.Text,
		DeliveredByHost: presentation.DeliveredByHost,
		Response:        presentation.Response,
	}, nil
}

func canonicalAgentCommand(invocation Invocation) string {
	return invocation.CanonicalCommand()
}

func agentCommandRequiresHostDelivery(invocation Invocation, response Response) bool {
	if response.Kind == ResponseKindAuthWait {
		return true
	}
	invocation, ok := withDescriptor(invocation)
	if !ok || invocation.Capability.Present == nil {
		return false
	}
	return invocation.Capability.Present(invocation, response, invocation.Capability.PolicyFor(invocation)).DeliveredByHost
}

func agentCommandNeedsConfirmation(invocation Invocation) bool {
	return invocation.Policy().Confirmation == ConfirmUser
}
