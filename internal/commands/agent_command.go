package commands

import (
	"context"

	"github.com/Life-USTC/Bot/internal/store"
)

// AgentCommandResult is the host-authoritative result of an Agent-requested
// command. Response is retained for application-layer delivery of images and
// private values; it is deliberately excluded from model-facing JSON.
type AgentCommandResult struct {
	Status               string   `json:"status"`
	Command              string   `json:"command,omitempty"`
	Kind                 string   `json:"kind,omitempty"`
	Text                 string   `json:"text,omitempty"`
	ConfirmationRequired bool     `json:"confirmationRequired,omitempty"`
	DeliveredByHost      bool     `json:"deliveredByHost,omitempty"`
	Response             Response `json:"-"`
}

// ExecuteCapabilityForAgent executes one validated structured capability.
// This is the Agent boundary; command strings remain a user-facing syntax.
func (h Handler) ExecuteCapabilityForAgent(ctx context.Context, input Input, id CapabilityID, args []string) (AgentCommandResult, error) {
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return AgentCommandResult{Status: "invalid_arguments", Kind: string(id), Text: "能力参数无效。"}, nil
	}
	return h.executeInvocationForAgent(ctx, input, invocation)
}

func (h Handler) executeInvocationForAgent(ctx context.Context, input Input, invocation Invocation) (AgentCommandResult, error) {
	if store.IsGroupConversation(input.Identity) && !h.groupCommandAllowed(invocation) {
		return AgentCommandResult{
			Status: "forbidden", Command: canonicalAgentCommand(invocation),
			Kind: invocation.Name, Text: "此功能只能在私聊使用。",
		}, nil
	}
	command := canonicalAgentCommand(invocation)
	policy := invocation.Capability.PolicyFor(invocation)
	if policy.Confirmation == ConfirmUser {
		return AgentCommandResult{
			Status:               "confirmation_required",
			Command:              command,
			Kind:                 invocation.Name,
			Text:                 "需要确认：" + command + "\n回复 ok 后执行。",
			ConfirmationRequired: true,
		}, nil
	}

	response, handled := h.executeInvocation(ctx, input, invocation)
	if !handled {
		return AgentCommandResult{Status: "not_found", Command: command, Kind: invocation.Name, Text: "宿主无法执行这条命令。"}, nil
	}
	presentation := invocation.Capability.Present(invocation, response, policy)
	return AgentCommandResult{
		Status:          "ok",
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
	invocation, ok := withDescriptor(invocation)
	if !ok || invocation.Capability.Present == nil {
		return false
	}
	return invocation.Capability.Present(invocation, response, invocation.Capability.PolicyFor(invocation)).DeliveredByHost
}

func agentCommandNeedsConfirmation(invocation Invocation) bool {
	return invocation.Policy().Confirmation == ConfirmUser
}
