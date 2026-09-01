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
	var err error
	ctx, err = ensureCapabilityJobLease(ctx, s.handler.Store, jobID)
	if err != nil {
		return "", err
	}

	invocations := commands.ExpandMutationInvocations(invocation)
	callID := strings.TrimSpace(compose.GetToolCallID(ctx))
	if callID == "" {
		callID = fmt.Sprintf("capability-%d", jobID)
	}
	state := capabilityInterruptState{ExecutionIDs: make([]string, 0, len(invocations))}
	descriptions := make([]commands.CapabilityInvocationDescription, 0, len(invocations))
	for _, item := range invocations {
		description, err := s.handler.DescribeInvocation(ctx, commands.Input{Identity: ident, SuppressLog: true}, item.ID(), item.Args)
		if err != nil {
			return "", fmt.Errorf("describe capability %q: %w", item.ID(), err)
		}
		if !description.ConfirmationRequired {
			return "", fmt.Errorf("capability %q bypassed confirmation", item.ID())
		}
		descriptions = append(descriptions, description)
	}
	prepares := make([]store.CapabilityExecutionPrepare, 0, len(descriptions))
	for index, description := range descriptions {
		item := description.Invocation
		receipt := commands.ReceiptForInvocation(item)
		if description.Receipt != nil {
			receipt = *description.Receipt
		}
		prepares = append(prepares, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID, LeaseToken: store.ConversationJobLeaseFromContext(ctx, jobID), Sequence: index,
			DedupeKey:  capabilityExecutionDedupeKey(jobID, callID, item),
			ToolCallID: callID, Capability: string(item.ID()), Arguments: append([]string(nil), item.Args...),
			Effect: string(item.Policy().Effect), Receipt: receipt, RequiresConfirmation: true,
		})
	}
	executions, _, err := s.handler.Store.PrepareCapabilityExecutions(ctx, prepares)
	if err != nil {
		return "", err
	}
	for _, execution := range executions {
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
		ctx, err = ensureCapabilityJobLease(ctx, s.handler.Store, jobID)
		if err != nil {
			return "", "", false, err
		}
		var created bool
		execution, created, err = s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID, LeaseToken: store.ConversationJobLeaseFromContext(ctx, jobID),
			DedupeKey:  capabilityExecutionDedupeKey(jobID, toolCallID, invocation),
			ToolCallID: toolCallID, Capability: string(invocation.ID()), Arguments: append([]string(nil), invocation.Args...),
			Effect: string(invocation.Policy().Effect), Receipt: commands.ReceiptForInvocation(invocation),
		})
		if err != nil {
			return "", "", false, err
		}
		executionID = execution.ID
		if !created && execution.State != store.CapabilityExecutionApproved && execution.State != store.CapabilityExecutionWaitingAuth && execution.State != store.CapabilityExecutionRunning {
			return capabilityExecutionModelResult(execution), execution.ID, execution.State == store.CapabilityExecutionWaitingAuth, nil
		}
		if execution.State != store.CapabilityExecutionRunning {
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, jobID, store.ConversationJobLeaseFromContext(ctx, jobID))
			if err != nil {
				return "", executionID, false, err
			}
			if !execute {
				if claimed.State == store.CapabilityExecutionRunning && !capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
					if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, claimed.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
						return "", executionID, false, err
					}
					execution, _, err = s.handler.Store.CapabilityExecution(ctx, claimed.ID)
					if err != nil {
						return "", executionID, false, err
					}
					return capabilityExecutionModelResult(execution), execution.ID, false, nil
				}
				if capabilityExecutionTerminal(claimed.State) {
					return capabilityExecutionModelResult(claimed), executionID, false, nil
				}
				if claimed.State == store.CapabilityExecutionWaitingAuth {
					return "", executionID, true, nil
				}
				return "", executionID, false, errors.New("capability execution claim was lost while the operation is still running")
			}
			execution = claimed
		} else if !created && capabilityExecutionIsRead(execution) && !capabilityExecutionHasStaleLease(ctx, execution) {
			return "", executionID, false, errors.New("capability execution is already running")
		} else if !capabilityExecutionIsRead(execution) {
			if capabilityExecutionHasStaleLease(ctx, execution) {
				if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
					return "", executionID, false, err
				}
				execution, _, err = s.handler.Store.CapabilityExecution(ctx, execution.ID)
				if err != nil {
					return "", executionID, false, err
				}
				return capabilityExecutionModelResult(execution), execution.ID, false, nil
			}
			return "", executionID, false, errors.New("capability execution is already running")
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
			if capabilityOutcomeIsUnknown(outcome) {
				_, _ = s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, text, "capability returned an unknown outcome")
			} else {
				_, _ = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", deliveryErr)
			}
		}
		return "", executionID, false, deliveryErr
	}
	if tracked {
		if capabilityOutcomeIsUnknown(outcome) {
			if _, err := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, text, "capability returned an unknown outcome"); err != nil {
				return "", executionID, false, err
			}
			return text, executionID, false, nil
		}
		var outcomeErr error
		if outcome.Status != commands.CapabilityOutcomeSuccess {
			outcomeErr = capabilityExecutionDiagnostic(outcome.Status)
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
	jobID, err := capabilityJobIDForExecutions(ctx, s.handler.Store, state.ExecutionIDs)
	if err != nil {
		return "", err
	}
	leaseCtx, err := ensureCapabilityJobLease(ctx, s.handler.Store, jobID)
	if err != nil {
		return "", err
	}
	ctx = leaseCtx
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
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, store.ConversationJobLeaseFromContext(ctx, execution.JobID))
			if err != nil {
				return "", err
			}
			if !execute {
				if claimed.State == store.CapabilityExecutionRunning && capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
					var waiting bool
					execution, waiting, err = s.executeApprovedCapability(ctx, claimed, ident, sendResponse)
					if err != nil {
						return "", err
					}
					authWait = authWait || waiting
				} else if claimed.State == store.CapabilityExecutionRunning && !capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
					if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, claimed.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
						return "", err
					}
					execution, _, err = s.handler.Store.CapabilityExecution(ctx, claimed.ID)
					if err != nil {
						return "", err
					}
				} else if claimed.State == store.CapabilityExecutionWaitingAuth {
					execution = claimed
					authWait = true
				} else if !capabilityExecutionTerminal(claimed.State) {
					return "", fmt.Errorf("capability execution %s claim was lost while state is %s", claimed.ID, claimed.State)
				} else {
					execution = claimed
				}
				break
			}
			var waiting bool
			execution, waiting, err = s.executeApprovedCapability(ctx, claimed, ident, sendResponse)
			if err != nil {
				return "", err
			}
			authWait = authWait || waiting
		case store.CapabilityExecutionRunning:
			if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
				var waiting bool
				execution, waiting, err = s.executeApprovedCapability(ctx, execution, ident, sendResponse)
				if err != nil {
					return "", err
				}
				authWait = authWait || waiting
			} else if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
				if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
					return "", err
				}
				execution, _, err = s.handler.Store.CapabilityExecution(ctx, execution.ID)
				if err != nil {
					return "", err
				}
			} else {
				return "", fmt.Errorf("capability execution %s is still running", execution.ID)
			}
		}
		if !capabilityExecutionTerminal(execution.State) && execution.State != store.CapabilityExecutionWaitingAuth {
			return "", fmt.Errorf("capability execution %s is not terminal: %s", execution.ID, execution.State)
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

func capabilityJobIDForExecutions(ctx context.Context, db *store.Store, ids []string) (int64, error) {
	for _, id := range ids {
		execution, found, err := db.CapabilityExecution(ctx, id)
		if err != nil {
			return 0, err
		}
		if found {
			return execution.JobID, nil
		}
	}
	return 0, errors.New("capability execution batch has no persisted job")
}

func ensureCapabilityJobLease(ctx context.Context, db *store.Store, jobID int64) (context.Context, error) {
	if jobID <= 0 {
		return ctx, errors.New("capability execution job id is invalid")
	}
	token := strings.TrimSpace(store.ConversationJobLeaseFromContext(ctx, jobID))
	if token == "" {
		return ctx, errors.New("capability execution requires the current conversation job lease")
	}
	job, err := db.GetConversationJob(ctx, jobID)
	if err != nil {
		return ctx, err
	}
	if job == nil {
		return ctx, fmt.Errorf("capability execution job %d is missing", jobID)
	}
	if job.State != store.ConversationJobStateRunning || strings.TrimSpace(job.LeaseToken) != token {
		return ctx, errors.New("capability execution lease is no longer current")
	}
	return ctx, nil
}

func capabilityExecutionIsRead(execution store.CapabilityExecution) bool {
	return strings.EqualFold(strings.TrimSpace(execution.Effect), string(commands.EffectRead))
}

func capabilityExecutionHasStaleLease(ctx context.Context, execution store.CapabilityExecution) bool {
	owner := strings.TrimSpace(execution.LeaseToken)
	current := strings.TrimSpace(store.ConversationJobLeaseFromContext(ctx, execution.JobID))
	return owner != "" && owner != current
}

const capabilityOutcomeUnknown commands.CapabilityOutcomeStatus = "unknown"

func capabilityOutcomeIsUnknown(outcome commands.CapabilityOutcome) bool {
	return outcome.Status == capabilityOutcomeUnknown
}

func capabilityExecutionDiagnostic(status commands.CapabilityOutcomeStatus) error {
	return fmt.Errorf("capability returned %s outcome", status)
}

func capabilityExecutionTerminal(state store.CapabilityExecutionState) bool {
	switch state {
	case store.CapabilityExecutionSucceeded,
		store.CapabilityExecutionFailed,
		store.CapabilityExecutionDenied,
		store.CapabilityExecutionUnknown,
		store.CapabilityExecutionCancelled,
		store.CapabilityExecutionExpired:
		return true
	default:
		return false
	}
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
			if capabilityOutcomeIsUnknown(outcome) {
				finished, finishErr := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, text, "capability returned an unknown outcome")
				return finished, false, finishErr
			}
			finished, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
			return finished, false, finishErr
		}
		if text == "" {
			text = "结果已由宿主发送给用户。"
		}
	}
	if capabilityOutcomeIsUnknown(outcome) {
		finished, err := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, text, "capability returned an unknown outcome")
		return finished, false, err
	}
	var outcomeErr error
	if outcome.Status != commands.CapabilityOutcomeSuccess {
		outcomeErr = capabilityExecutionDiagnostic(outcome.Status)
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
	case store.CapabilityExecutionSucceeded, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
		return strings.TrimSpace(execution.Result)
	case store.CapabilityExecutionDenied, store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
		reason := strings.TrimSpace(execution.Error)
		if reason == "" {
			reason = "用户拒绝执行"
		}
		return reason
	case store.CapabilityExecutionRunning:
		return ""
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
