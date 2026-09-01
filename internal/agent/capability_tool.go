package agent

import (
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

type capabilityInterruptState struct {
	ExecutionIDs []string
}

type capabilityInterruptInfo struct {
	Kind         string
	ExecutionIDs []string
}

const (
	capabilityInterruptConfirmation = "confirmation"
	capabilityInterruptAuth         = "auth"
)

func init() {
	gob.Register(capabilityInterruptState{})
	gob.Register(capabilityInterruptInfo{})
}

func (s *Service) invokeHostCapability(
	ctx context.Context,
	input hostCapabilityInput,
	ident store.Identity,
	jobID int64,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (string, error) {
	if wasInterrupted, hasState, state := tool.GetInterruptState[capabilityInterruptState](ctx); wasInterrupted {
		if !hasState || len(state.ExecutionIDs) == 0 {
			return "", errors.New("confirmed capability checkpoint has no operation state")
		}
		return s.resumeHostCapability(ctx, state, ident, sendResponse)
	}

	id := commands.CapabilityID(strings.TrimSpace(input.Capability))
	invocation, valid := commands.NewInvocation(id, input.Arguments)
	if !valid {
		outcome, err := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true}, id, input.Arguments)
		if err != nil {
			return "", err
		}
		if outcome.Status != commands.CapabilityOutcomeSuccess {
			toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
		}
		presentation := s.handler.PresentCapabilityOutcome(commands.Invocation{Name: string(id), Args: append([]string(nil), input.Arguments...)}, outcome)
		return strings.TrimSpace(presentation.Text), nil
	}
	policy := invocation.Policy()
	if policy.Confirmation != commands.ConfirmUser {
		result, executionID, authWait, err := s.executeUnconfirmedHostCapability(ctx, invocation, ident, jobID, compose.GetToolCallID(ctx), sendResponse)
		if err != nil || !authWait {
			return result, err
		}
		state := capabilityInterruptState{ExecutionIDs: []string{executionID}}
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptAuth, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
		}, state)
	}
	if jobID <= 0 || s.handler.Store == nil {
		return "", errors.New("durable confirmation requires a persisted conversation job")
	}

	invocations := commands.ExpandMutationInvocations(invocation)
	callID := strings.TrimSpace(compose.GetToolCallID(ctx))
	if callID == "" {
		callID = fmt.Sprintf("capability-%d", jobID)
	}
	state := capabilityInterruptState{ExecutionIDs: make([]string, 0, len(invocations))}
	for index, item := range invocations {
		description, err := s.handler.DescribeInvocation(ctx, commands.Input{Identity: ident, SuppressLog: true}, item.ID(), item.Args)
		if err != nil {
			outcome, executeErr := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true}, item.ID(), item.Args)
			if executeErr != nil {
				return "", executeErr
			}
			presentation := s.handler.PresentCapabilityOutcome(item, outcome)
			return strings.TrimSpace(presentation.Text), nil
		}
		receipt := commands.ReceiptForInvocation(item)
		if description.Receipt != nil {
			receipt = *description.Receipt
		}
		execution, _, err := s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID, Sequence: index,
			DedupeKey:  capabilityExecutionDedupeKey(jobID, callID, item),
			ToolCallID: callID, Capability: string(item.ID()), Arguments: append([]string(nil), item.Args...),
			Effect: string(item.Policy().Effect), Receipt: receipt, RequiresConfirmation: true,
		})
		if err != nil {
			return "", err
		}
		state.ExecutionIDs = append(state.ExecutionIDs, execution.ID)
	}
	return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
		Kind: capabilityInterruptConfirmation, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
	}, state)
}

func (s *Service) executeUnconfirmedHostCapability(
	ctx context.Context,
	invocation commands.Invocation,
	ident store.Identity,
	jobID int64,
	toolCallID string,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (modelResult string, executionID string, authWait bool, err error) {
	var execution store.CapabilityExecution
	tracked := s.handler.Store != nil && jobID > 0
	if tracked {
		var err error
		execution, _, err = s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID,
			DedupeKey:  capabilityExecutionDedupeKey(jobID, toolCallID, invocation),
			ToolCallID: toolCallID, Capability: string(invocation.ID()), Arguments: append([]string(nil), invocation.Args...),
			Effect: string(invocation.Policy().Effect), Receipt: commands.ReceiptForInvocation(invocation),
		})
		if err != nil {
			return "", "", false, err
		}
		executionID = execution.ID
		if execution.State != store.CapabilityExecutionRunning {
			return capabilityExecutionModelResult(execution), execution.ID, execution.State == store.CapabilityExecutionWaitingAuth, nil
		}
	}
	outcome, err := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true}, invocation.ID(), invocation.Args)
	if err != nil {
		if tracked {
			_, _ = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
		}
		return "", executionID, false, err
	}
	if outcome.Status != commands.CapabilityOutcomeSuccess && outcome.Status != commands.CapabilityOutcomeAuthRequired {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
	}
	presentation := s.handler.PresentCapabilityOutcome(invocation, outcome)
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		if !tracked {
			text, deliveryErr := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse)
			return text, "", false, deliveryErr
		}
		if _, err := s.handler.Store.DeferCapabilityExecutionForAuth(ctx, execution.ID); err != nil {
			return "", executionID, false, err
		}
		if _, err := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse); err != nil {
			return "", executionID, false, err
		}
		return "", executionID, true, nil
	}
	text, deliveryErr := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse)
	if deliveryErr != nil {
		if tracked {
			_, _ = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", deliveryErr)
		}
		return "", executionID, false, deliveryErr
	}
	if tracked {
		var outcomeErr error
		if outcome.Status != commands.CapabilityOutcomeSuccess {
			outcomeErr = errors.New(strings.TrimSpace(text))
		}
		if _, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, text, outcomeErr); err != nil {
			return "", executionID, false, err
		}
	}
	return text, executionID, false, nil
}

func (s *Service) resumeHostCapability(
	ctx context.Context,
	state capabilityInterruptState,
	ident store.Identity,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (string, error) {
	if s.handler.Store == nil {
		return "", errors.New("capability execution store is unavailable")
	}
	pending := false
	authWait := false
	results := make([]string, 0, len(state.ExecutionIDs))
	for _, executionID := range state.ExecutionIDs {
		execution, found, err := s.handler.Store.CapabilityExecution(ctx, executionID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("capability execution %s is missing", executionID)
		}
		switch execution.State {
		case store.CapabilityExecutionAwaitingConfirmation:
			pending = true
			continue
		case store.CapabilityExecutionApproved, store.CapabilityExecutionWaitingAuth:
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecution(ctx, execution.ID)
			if err != nil {
				return "", err
			}
			if !execute {
				execution = claimed
				break
			}
			var waiting bool
			execution, waiting, err = s.executeApprovedCapability(ctx, claimed, ident, sendResponse)
			if err != nil {
				return "", err
			}
			authWait = authWait || waiting
		case store.CapabilityExecutionRunning:
			if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
				return "", err
			}
			execution, _, err = s.handler.Store.CapabilityExecution(ctx, execution.ID)
			if err != nil {
				return "", err
			}
		}
		if execution.State != store.CapabilityExecutionWaitingAuth {
			results = append(results, capabilityExecutionModelResult(execution))
		}
	}
	if authWait {
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptAuth, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
		}, state)
	}
	if pending {
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptConfirmation, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
		}, state)
	}
	return strings.TrimSpace(strings.Join(results, "\n\n")), nil
}

func (s *Service) executeApprovedCapability(
	ctx context.Context,
	execution store.CapabilityExecution,
	ident store.Identity,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (store.CapabilityExecution, bool, error) {
	invocation, valid := commands.RestoreInvocation(commands.CapabilityID(execution.Capability), execution.Arguments)
	if !valid {
		runErr := errors.New("宿主无法恢复已确认的操作")
		finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", runErr)
		return finished, false, err
	}
	description := commands.CapabilityInvocationDescription{
		Invocation: invocation, Policy: invocation.Policy(), ConfirmationRequired: invocation.Policy().Confirmation == commands.ConfirmUser,
		Receipt: &execution.Receipt,
	}
	outcome, err := s.handler.ExecuteApprovedInvocation(ctx, commands.Input{Identity: ident, SuppressLog: true}, description)
	if err != nil {
		finished, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
		return finished, false, finishErr
	}
	if outcome.Status != commands.CapabilityOutcomeSuccess && outcome.Status != commands.CapabilityOutcomeAuthRequired {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
	}
	presentation := s.handler.PresentCapabilityOutcome(invocation, outcome)
	text := strings.TrimSpace(presentation.Text)
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		deferred, err := s.handler.Store.DeferCapabilityExecutionForAuth(ctx, execution.ID)
		if err != nil {
			return deferred, false, err
		}
		if presentation.DeliveredByHost {
			if sendResponse == nil {
				return deferred, false, errors.New("host response sender is unavailable")
			}
			if err := sendResponse(ctx, ident, presentation.Response); err != nil {
				return deferred, false, err
			}
		}
		return deferred, true, nil
	}
	if presentation.DeliveredByHost {
		if sendResponse == nil {
			runErr := errors.New("host response sender is unavailable")
			finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", runErr)
			return finished, false, err
		}
		if err := sendResponse(ctx, ident, presentation.Response); err != nil {
			finished, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
			return finished, false, finishErr
		}
		if text == "" {
			text = "结果已由宿主发送给用户。"
		}
	}
	var outcomeErr error
	if outcome.Status != commands.CapabilityOutcomeSuccess {
		outcomeErr = errors.New(text)
	}
	finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, text, outcomeErr)
	return finished, false, err
}

func deliverCapabilityPresentation(
	ctx context.Context,
	ident store.Identity,
	presentation commands.CapabilityPresentation,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (string, error) {
	text := strings.TrimSpace(presentation.Text)
	if !presentation.DeliveredByHost {
		return text, nil
	}
	if sendResponse == nil {
		return "", errors.New("host response sender is unavailable")
	}
	if err := sendResponse(ctx, ident, presentation.Response); err != nil {
		return "", err
	}
	if text == "" {
		text = "结果已由宿主发送给用户。"
	}
	return text, nil
}

func capabilityExecutionModelResult(execution store.CapabilityExecution) string {
	switch execution.State {
	case store.CapabilityExecutionSucceeded:
		return strings.TrimSpace(execution.Result)
	case store.CapabilityExecutionDenied:
		reason := strings.TrimSpace(execution.Error)
		if reason == "" {
			reason = "用户拒绝执行"
		}
		return reason
	case store.CapabilityExecutionFailed:
		return strings.TrimSpace(execution.Error)
	case store.CapabilityExecutionUnknown, store.CapabilityExecutionRunning:
		return strings.TrimSpace(execution.Error)
	default:
		return "操作尚未执行"
	}
}

func capabilityExecutionDedupeKey(jobID int64, toolCallID string, invocation commands.Invocation) string {
	if invocation.Policy().Effect != commands.EffectRead {
		payload, _ := json.Marshal(struct {
			Capability commands.CapabilityID `json:"capability"`
			Arguments  []string              `json:"arguments"`
		}{Capability: invocation.ID(), Arguments: invocation.Args})
		digest := sha256.Sum256(payload)
		return fmt.Sprintf("conversation-job:%d:mutation:%s", jobID, hex.EncodeToString(digest[:16]))
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID != "" {
		return fmt.Sprintf("conversation-job:%d:tool:%s", jobID, toolCallID)
	}
	return fmt.Sprintf("conversation-job:%d:read:%s", jobID, invocation.CanonicalCommand())
}
