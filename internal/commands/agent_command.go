package commands

import (
	"context"
	"errors"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

const agentConfirmationTTL = 15 * time.Minute

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

// ExecuteForAgent parses and executes one host command without feeding it back
// through App.Process. Mutation confirmation and result exposure are resolved
// from the invocation's descriptor policy.
func (h Handler) ExecuteForAgent(ctx context.Context, input Input) (AgentCommandResult, error) {
	input.Text = stripCQCodes(input.Text)
	if isConfirmationOK(input.Text) {
		return AgentCommandResult{Status: "forbidden", Text: "Agent 不能代替用户确认操作；请等待用户回复 ok。"}, nil
	}
	invocation, ok := h.parse(input.Text)
	if !ok {
		return AgentCommandResult{Status: "not_found", Text: "宿主无法识别这条命令。"}, nil
	}
	if h.hasAdditionalCommandLine(input.Text) {
		return AgentCommandResult{Status: "forbidden", Text: "一次只能执行一条宿主命令。"}, nil
	}
	if store.IsGroupConversation(input.Identity) && !h.groupCommandAllowed(invocation) {
		return AgentCommandResult{
			Status: "forbidden", Command: canonicalAgentCommand(invocation),
			Kind: invocation.Name, Text: "此功能只能在私聊使用。",
		}, nil
	}
	command := canonicalAgentCommand(invocation)
	policy := invocation.Capability.PolicyFor(invocation)
	if policy.Confirmation == ConfirmUser {
		if h.Store == nil {
			return AgentCommandResult{}, errors.New("confirmation store is unavailable")
		}
		if _, err := h.Store.SavePendingConfirmation(ctx, input.Identity, command, "agent", agentConfirmationTTL); err != nil {
			return AgentCommandResult{}, err
		}
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
