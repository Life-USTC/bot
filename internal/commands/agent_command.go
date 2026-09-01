package commands

import (
	"context"
	"errors"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	AgentCommandStatusSuccess              = "success"
	AgentCommandStatusFailed               = "failed"
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
	Receipt              *CapabilityReceipt       `json:"receipt,omitempty"`
	Outcome              CapabilityOutcome        `json:"-"`
	Response             Response                 `json:"-"`
}

// ExecuteCapability validates, applies host privacy policy, resolves a
// host-derived receipt, and either prepares a confirmation or executes the
// capability. The returned Status is independent from Response.Text.
func (h Handler) ExecuteCapability(ctx context.Context, input Input, id CapabilityID, args []string) (CapabilityOutcome, error) {
	descriptor, found := CapabilityDescriptorFor(id)
	if !found {
		return NotFoundOutcome(Response{Text: "没有找到这个能力。请使用工具描述中的稳定 capability ID。", Kind: string(id)}), nil
	}
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: string(descriptor.ID)}), nil
	}
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), nil
	}
	description, err := h.DescribeInvocation(ctx, input, id, args)
	if err != nil {
		if errors.Is(err, errCapabilityForbidden) {
			return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), nil
		}
		if errors.Is(err, errCapabilityInvalidInput) || errors.Is(err, errCapabilityMutationMustExpand) {
			return InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: invocation.Name}), nil
		}
		if errors.Is(err, errCapabilityReceiptTargetNotFound) {
			return NotFoundOutcome(Response{Text: "没有找到可匹配的教学班。", Kind: invocation.Name}), nil
		}
		return FailedOutcome(Response{Text: "能力目标查找失败：" + friendlyError(err), Kind: invocation.Name}), nil
	}
	return h.executeDescribedCapability(ctx, input, description, false), nil
}

// ExecuteInvocation executes an already normalized invocation through the
// same typed boundary as ExecuteCapability.
func (h Handler) ExecuteInvocation(ctx context.Context, input Input, invocation Invocation) (CapabilityOutcome, error) {
	original := invocation
	invocation, ok := withDescriptor(invocation)
	if !ok {
		return NotFoundOutcome(Response{Text: "没有找到这个能力。", Kind: original.Name}), nil
	}
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), nil
	}
	description, err := h.DescribeInvocation(ctx, input, invocation.ID(), invocation.Args)
	if err != nil {
		if errors.Is(err, errCapabilityForbidden) {
			return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), nil
		}
		if errors.Is(err, errCapabilityInvalidInput) || errors.Is(err, errCapabilityMutationMustExpand) {
			return InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: invocation.Name}), nil
		}
		if errors.Is(err, errCapabilityReceiptTargetNotFound) {
			return NotFoundOutcome(Response{Text: "没有找到可匹配的教学班。", Kind: invocation.Name}), nil
		}
		return FailedOutcome(Response{Text: "能力目标查找失败：" + friendlyError(err), Kind: invocation.Name}), nil
	}
	return h.executeDescribedCapability(ctx, input, description, false), nil
}

// ExecuteApprovedInvocation executes a previously described invocation after
// host approval, retaining its frozen receipt subject.
func (h Handler) ExecuteApprovedInvocation(ctx context.Context, input Input, description CapabilityInvocationDescription) (CapabilityOutcome, error) {
	return h.executeDescribedCapability(ctx, input, description, true), nil
}

// ExecuteCapabilityForAgent executes one validated structured capability.
// ConfirmUser mutations are prepared but never executed by this method.
func (h Handler) ExecuteCapabilityForAgent(ctx context.Context, input Input, id CapabilityID, args []string) (AgentCommandResult, error) {
	outcome, err := h.ExecuteCapability(ctx, input, id, args)
	if err != nil {
		return AgentCommandResult{Status: AgentCommandStatusFailed, Text: err.Error()}, nil
	}
	invocation, ok := NewInvocation(id, args)
	if !ok {
		if restored, restoredOK := RestoreInvocation(id, args); restoredOK {
			invocation = restored
		} else {
			invocation = Invocation{Name: string(id), Args: append([]string(nil), args...)}
		}
	}
	result := h.agentResult(outcome, invocation)
	if result.Status == AgentCommandStatusInvalidInput {
		result.SuggestedCalls = CapabilityUsageExamples(id)
	}
	return result, nil
}

// ExecuteApprovedCapabilityForAgent executes a capability after the host has
// approved its confirmation. It bypasses only the confirmation gate; argument
// validation, shared-conversation privacy and authentication handling remain
// enforced.
func (h Handler) ExecuteApprovedCapabilityForAgent(ctx context.Context, input Input, id CapabilityID, args []string) (AgentCommandResult, error) {
	invocation, ok := NewInvocation(id, args)
	if !ok {
		result, err := h.ExecuteCapabilityForAgent(ctx, input, id, args)
		return result, err
	}
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return h.agentResult(ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), invocation), nil
	}
	description, err := h.DescribeInvocation(ctx, input, id, args)
	if err != nil {
		if errors.Is(err, errCapabilityForbidden) {
			return h.agentResult(ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), invocation), nil
		}
		if errors.Is(err, errCapabilityInvalidInput) || errors.Is(err, errCapabilityMutationMustExpand) {
			return h.agentResult(InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: invocation.Name}), invocation), nil
		}
		if errors.Is(err, errCapabilityReceiptTargetNotFound) {
			return h.agentResult(NotFoundOutcome(Response{Text: "没有找到可匹配的教学班。", Kind: invocation.Name}), invocation), nil
		}
		return h.agentResult(FailedOutcome(Response{Text: "能力目标查找失败：" + friendlyError(err), Kind: invocation.Name}), invocation), nil
	}
	outcome := h.executeDescribedCapability(ctx, input, description, true)
	return h.agentResult(outcome, description.Invocation), nil
}

// ExecuteApprovedInvocationForAgent reuses a previously frozen description,
// including its receipt subject, when the host confirms a mutation.
func (h Handler) ExecuteApprovedInvocationForAgent(ctx context.Context, input Input, description CapabilityInvocationDescription) (AgentCommandResult, error) {
	outcome := h.executeDescribedCapability(ctx, input, description, true)
	return h.agentResult(outcome, description.Invocation), nil
}

func (h Handler) executeDescribedCapability(ctx context.Context, input Input, description CapabilityInvocationDescription, approved bool) CapabilityOutcome {
	// Rebind the frozen invocation to the current registry entry and validate
	// its arguments again. A confirmation token may bypass only the explicit
	// confirmation gate; it must not turn a stale or hand-crafted description
	// into an executable command.
	original := description.Invocation
	invocation, valid := NewInvocation(original.ID(), original.Args)
	if !valid {
		return InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: original.Name})
	}
	if err := validateReceiptInvocation(invocation); err != nil {
		return InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: invocation.Name})
	}
	invocation.Raw = original.Raw
	invocation.NaturalRoute = original.NaturalRoute
	policy := invocation.Policy()
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name})
	}
	if policy.Confirmation == ConfirmUser && !approved {
		return CapabilityOutcome{
			Status:               CapabilityOutcomeSuccess,
			Response:             Response{Text: "需要确认：" + canonicalAgentCommand(invocation) + "\n回复 ok 后执行。", Kind: invocation.Name},
			ConfirmationRequired: true,
			Receipt:              description.Receipt,
		}
	}
	outcome, handled := h.executeInvocationOutcome(ctx, input, invocation)
	if !handled {
		return CapabilityOutcome{Status: CapabilityOutcomeNotFound, Response: Response{Text: "宿主无法执行这条命令。", Kind: invocation.Name}, Receipt: description.Receipt}
	}
	outcome.Receipt = description.Receipt
	return outcome
}

// executeInvocationForAgent remains the internal adapter used by older host
// callers; all status decisions now flow through the typed boundary.
func (h Handler) executeInvocationForAgent(ctx context.Context, input Input, invocation Invocation) (AgentCommandResult, error) {
	description, err := h.DescribeInvocation(ctx, input, invocation.ID(), invocation.Args)
	if err != nil {
		if errors.Is(err, errCapabilityForbidden) {
			return h.agentResult(ForbiddenOutcome(Response{Text: "此功能只能在私聊使用。", Kind: invocation.Name}), invocation), nil
		}
		if errors.Is(err, errCapabilityInvalidInput) || errors.Is(err, errCapabilityMutationMustExpand) {
			return h.agentResult(InvalidInputOutcome(Response{Text: "能力参数无效。请使用 suggestedCalls 中的 capability 和 arguments 修正调用。", Kind: invocation.Name}), invocation), nil
		}
		if errors.Is(err, errCapabilityReceiptTargetNotFound) {
			return h.agentResult(NotFoundOutcome(Response{Text: "没有找到可匹配的教学班。", Kind: invocation.Name}), invocation), nil
		}
		return h.agentResult(FailedOutcome(Response{Text: "能力目标查找失败：" + friendlyError(err), Kind: invocation.Name}), invocation), nil
	}
	outcome := h.executeDescribedCapability(ctx, input, description, false)
	return h.agentResult(outcome, description.Invocation), nil
}

func (h Handler) agentResult(outcome CapabilityOutcome, invocation Invocation) AgentCommandResult {
	outcome = normalizeOutcome(outcome)
	result := AgentCommandResult{
		OK:       outcome.OK(),
		Status:   string(outcome.Status),
		Command:  canonicalAgentCommand(invocation),
		Kind:     outcome.Response.Kind,
		Receipt:  outcome.Receipt,
		Outcome:  outcome,
		Response: outcome.Response,
	}
	if result.Kind == "" {
		result.Kind = invocation.Name
	}
	if outcome.ConfirmationRequired {
		result.OK = false
		result.Status = AgentCommandStatusConfirmationRequired
		result.ConfirmationRequired = true
	}
	if outcome.Status == CapabilityOutcomeAuthRequired {
		result.OK = false
		result.Text = "需要登录；登录提示已由宿主安全发送，授权后会继续刚才的请求。"
		result.DeliveredByHost = true
		return result
	}
	presented := false
	if invocation.Capability != nil && invocation.Capability.Present != nil {
		presented = true
		presentation := invocation.Capability.Present(invocation, outcome.Response, invocation.Policy())
		result.Text = presentation.Text
		result.DeliveredByHost = presentation.DeliveredByHost
		result.Response = presentation.Response
	}
	if !presented && result.Text == "" && outcome.Response.Text != "" && outcome.Status != CapabilityOutcomeAuthRequired {
		result.Text = outcome.Response.Text
	}
	return result
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
