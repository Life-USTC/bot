package commands

import (
	"context"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// ExecuteCapability validates, applies the host privacy policy, and executes
// one normalized capability. Confirmation is deliberately owned by the
// caller: direct commands execute immediately, while the Agent host gates
// non-read effects before calling this method.
func (h Handler) ExecuteCapability(ctx context.Context, input Input, id CapabilityID, args []string) (CapabilityOutcome, error) {
	descriptor, found := CapabilityDescriptorFor(id)
	if !found {
		return NotFoundOutcome(Response{
			Text: "没有找到这个能力。请先查询 Bot 命令文档。",
			Data: map[string]any{"type": "unknown_capability", "id": id},
			Kind: string(id),
		}), nil
	}
	candidate, _ := RestoreInvocation(id, args)
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(candidate) {
		return ForbiddenOutcome(Response{
			Text: "此功能只能在私聊使用。",
			Data: map[string]any{"type": "forbidden_capability", "id": id},
			Kind: string(id),
		}), nil
	}
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return InvalidInputOutcome(invalidCapabilityUsageResponse(descriptor.ID)), nil
	}
	outcome, handled := h.executeInvocationOutcome(ctx, input, invocation)
	if !handled {
		return NotFoundOutcome(Response{
			Text: "宿主无法执行这条命令。",
			Data: map[string]any{"type": "unhandled_capability", "id": id},
			Kind: string(id),
		}), nil
	}
	return outcome, nil
}

// PresentCapabilityOutcome applies only the descriptor's result-exposure
// decision. Status and Data remain host/model state; Text is presentation for
// delivery, while Response carries both forms without reconstructing Data.
func (h Handler) PresentCapabilityOutcome(invocation Invocation, outcome CapabilityOutcome) CapabilityPresentation {
	outcome = normalizeOutcome(outcome)
	presentation := CapabilityPresentation{Text: strings.TrimSpace(outcome.Response.Text), Response: outcome.Response}
	if invocation, ok := withDescriptor(invocation); ok && invocation.Capability.Present != nil {
		presentation = invocation.Capability.Present(invocation, outcome.Response, invocation.Policy())
		presentation.Text = strings.TrimSpace(presentation.Text)
	}
	if outcome.Status == CapabilityOutcomeAuthRequired {
		presentation.Text = "需要登录；登录提示已由宿主发送，授权后会自动继续刚才的请求。"
		presentation.DeliveredByHost = true
		presentation.Response = outcome.Response
	}
	return presentation
}
