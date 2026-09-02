package commands

import (
	"context"
	"errors"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// ExecuteCapability validates, applies the host privacy policy, resolves any
// authoritative receipt subject, and executes one normalized capability.
// Confirmation-required mutations are described but never executed here.
func (h Handler) ExecuteCapability(ctx context.Context, input Input, id CapabilityID, args []string) (CapabilityOutcome, error) {
	descriptor, found := CapabilityDescriptorFor(id)
	if !found {
		return NotFoundOutcome(Response{Text: "没有找到这个能力。请先查询 Bot 命令文档。", Kind: string(id)}), nil
	}
	candidate, _ := RestoreInvocation(id, args)
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(candidate) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: string(id)}), nil
	}
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return InvalidInputOutcome(invalidCapabilityUsageResponse(descriptor.ID)), nil
	}
	return h.executeCapabilityInvocation(ctx, input, invocation, false)
}

// ExecuteInvocation is the Invocation form of ExecuteCapability.
func (h Handler) ExecuteInvocation(ctx context.Context, input Input, invocation Invocation) (CapabilityOutcome, error) {
	original := invocation
	invocation, ok := withDescriptor(invocation)
	if !ok {
		return NotFoundOutcome(Response{Text: "没有找到这个能力。请先查询 Bot 命令文档。", Kind: original.Name}), nil
	}
	return h.executeCapabilityInvocation(ctx, input, invocation, false)
}

func (h Handler) executeCapabilityInvocation(ctx context.Context, input Input, invocation Invocation, approved bool) (CapabilityOutcome, error) {
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), nil
	}
	description, err := h.DescribeInvocation(ctx, input, invocation.ID(), invocation.Args)
	if err != nil {
		return capabilityDescriptionFailure(invocation, err), nil
	}
	description.Invocation.Raw = invocation.Raw
	description.Invocation.NaturalRoute = invocation.NaturalRoute
	return h.executeDescribedCapability(ctx, input, description, approved), nil
}

func capabilityDescriptionFailure(invocation Invocation, err error) CapabilityOutcome {
	switch {
	case errors.Is(err, errCapabilityForbidden):
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name})
	case errors.Is(err, errCapabilityInvalidInput), errors.Is(err, errCapabilityMutationMustExpand):
		return InvalidInputOutcome(invalidCapabilityUsageResponse(invocation.ID()))
	case errors.Is(err, errCapabilityReceiptTargetNotFound):
		return NotFoundOutcome(Response{Text: "没有找到可匹配的教学班。", Kind: invocation.Name})
	default:
		return FailedOutcome(Response{Text: "能力目标查找失败：" + friendlyError(err), Kind: invocation.Name})
	}
}

// ExecuteApprovedInvocation executes a previously frozen description after
// the host has consumed a real user approval. The approval bypasses only the
// confirmation gate; validation, privacy, and authentication still apply.
func (h Handler) ExecuteApprovedInvocation(ctx context.Context, input Input, description CapabilityInvocationDescription) (CapabilityOutcome, error) {
	return h.executeDescribedCapability(ctx, input, description, true), nil
}

func (h Handler) executeDescribedCapability(ctx context.Context, input Input, description CapabilityInvocationDescription, approved bool) CapabilityOutcome {
	original := description.Invocation
	invocation, valid := NewInvocation(original.ID(), original.Args)
	if !valid {
		return InvalidInputOutcome(invalidCapabilityUsageResponse(original.ID()))
	}
	if err := validateReceiptInvocation(invocation); err != nil {
		return InvalidInputOutcome(invalidCapabilityUsageResponse(invocation.ID()))
	}
	invocation.Raw = original.Raw
	invocation.NaturalRoute = original.NaturalRoute
	policy := invocation.Policy()
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name})
	}
	if policy.Confirmation == ConfirmUser && !approved {
		return CapabilityOutcome{Status: CapabilityOutcomeSuccess, ConfirmationRequired: true, Receipt: description.Receipt}
	}
	outcome, handled := h.executeInvocationOutcome(ctx, input, invocation)
	if !handled {
		return CapabilityOutcome{Status: CapabilityOutcomeNotFound, Response: Response{Text: "宿主无法执行这条命令。", Kind: invocation.Name}, Receipt: description.Receipt}
	}
	outcome.Receipt = description.Receipt
	return outcome
}

// PresentCapabilityOutcome applies only the descriptor's result-exposure
// decision. Status remains host state; Text is literal model-facing domain
// evidence, while Response is available for host-rendered delivery.
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
