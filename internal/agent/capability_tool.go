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
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

type capabilityInterruptState struct {
	ExecutionIDs []string
	ToolCallID   string
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
		if !hasState || len(state.ExecutionIDs) == 0 || strings.TrimSpace(state.ToolCallID) == "" {
			return "", errors.New("confirmed capability checkpoint has no operation state")
		}
		return s.resolveHostCapability(ctx, state, ident, sendResponse, true)
	}

	id := commands.CapabilityID(strings.TrimSpace(input.Capability))
	invocation, valid := commands.NewInvocation(id, input.Arguments)
	if !valid {
		outcome, err := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true, Origin: commands.InvocationOriginAgent}, id, input.Arguments)
		if err != nil {
			return "", err
		}
		if capabilityOutcomeIsToolError(outcome.Status) {
			toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
		}
		return encodeCapabilityOutcome(string(id), outcome), nil
	}
	policy := invocation.Policy()
	if policy.Effect != commands.EffectRead && (jobID <= 0 || s.handler.Store == nil) {
		return "", errors.New("mutations require a persisted conversation job")
	}
	if policy.Effect == commands.EffectRead {
		callID := capabilityToolCallID(ctx, jobID)
		result, executionID, authWait, err := s.executeUnconfirmedHostCapability(ctx, invocation, ident, jobID, callID, sendResponse)
		if err != nil || !authWait {
			return result, err
		}
		state := capabilityInterruptState{ExecutionIDs: []string{executionID}, ToolCallID: callID}
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
	callID := capabilityToolCallID(ctx, jobID)
	state := capabilityInterruptState{ExecutionIDs: make([]string, 0, len(invocations)), ToolCallID: callID}
	descriptions := make([]commands.CapabilityInvocationDescription, 0, len(invocations))
	for _, item := range invocations {
		description, err := s.handler.DescribeInvocation(ctx, commands.Input{Identity: ident, SuppressLog: true, Origin: commands.InvocationOriginAgent}, item.ID(), item.Args)
		if err != nil {
			if result, ok := commands.CapabilityPreflightFailure(item.ID(), err); ok {
				toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
				return result, nil
			}
			return "", fmt.Errorf("describe capability %q: %w", item.ID(), err)
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
			Effect: string(item.Policy().Effect), Receipt: receipt, RequiresConfirmation: item.Policy().Effect == commands.EffectDestructive,
		})
	}
	executions, _, err := s.handler.Store.PrepareCapabilityExecutions(ctx, prepares)
	if err != nil {
		return "", markDurableAgentStateError("prepare capability confirmations", err)
	}
	for _, execution := range executions {
		state.ExecutionIDs = append(state.ExecutionIDs, execution.ID)
	}
	return s.resolveHostCapability(ctx, state, ident, sendResponse, false)
}

func capabilityToolCallID(ctx context.Context, jobID int64) string {
	if callID := strings.TrimSpace(compose.GetToolCallID(ctx)); callID != "" {
		return callID
	}
	return fmt.Sprintf("capability-%d", jobID)
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
			return "", "", false, markDurableAgentStateError("prepare capability execution", err)
		}
		executionID = execution.ID
		if !created && execution.State != store.CapabilityExecutionApproved && execution.State != store.CapabilityExecutionWaitingAuth && execution.State != store.CapabilityExecutionRunning {
			return capabilityExecutionModelResult(execution), execution.ID, execution.State == store.CapabilityExecutionWaitingAuth, nil
		}
		if execution.State != store.CapabilityExecutionRunning {
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, jobID, store.ConversationJobLeaseFromContext(ctx, jobID))
			if err != nil {
				return "", executionID, false, markDurableAgentStateError("claim capability execution", err)
			}
			if !execute {
				if claimed.State == store.CapabilityExecutionRunning && !capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
					current, marked, err := markStaleCapabilityExecutionUnknown(ctx, s.handler.Store, claimed)
					if err != nil {
						return "", executionID, false, err
					}
					if !marked && !capabilityExecutionTerminal(current.State) {
						return "", executionID, false, errors.New("capability execution ownership changed while marking an interrupted mutation")
					}
					return capabilityExecutionModelResult(current), current.ID, false, nil
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
		} else if !created && capabilityExecutionIsRead(execution) {
			if !capabilityExecutionHasStaleLease(ctx, execution) {
				return "", executionID, false, errors.New("capability execution is already running")
			}
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, jobID, store.ConversationJobLeaseFromContext(ctx, jobID))
			if err != nil {
				return "", executionID, false, markDurableAgentStateError("recover capability read", err)
			}
			if !execute {
				if capabilityExecutionTerminal(claimed.State) {
					return capabilityExecutionModelResult(claimed), claimed.ID, false, nil
				}
				return "", executionID, false, errors.New("capability read ownership changed before recovery")
			}
			execution = claimed
		} else if !capabilityExecutionIsRead(execution) {
			if capabilityExecutionHasStaleLease(ctx, execution) {
				current, marked, err := markStaleCapabilityExecutionUnknown(ctx, s.handler.Store, execution)
				if err != nil {
					return "", executionID, false, err
				}
				if !marked && !capabilityExecutionTerminal(current.State) {
					return "", executionID, false, errors.New("capability execution ownership changed while marking an interrupted mutation")
				}
				return capabilityExecutionModelResult(current), current.ID, false, nil
			}
			return "", executionID, false, errors.New("capability execution is already running")
		}
	}
	outcome, err := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true, Origin: commands.InvocationOriginAgent}, invocation.ID(), invocation.Args)
	if err != nil {
		if tracked {
			if _, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", err); finishErr != nil {
				return "", executionID, false, markDurableAgentStateError("record capability failure", finishErr)
			}
		}
		return "", executionID, false, err
	}
	if capabilityOutcomeIsToolError(outcome.Status) {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
	}
	presentation := s.handler.PresentCapabilityOutcome(invocation, outcome)
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		if !tracked {
			_, deliveryErr := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse)
			return encodeCapabilityOutcome(string(invocation.ID()), outcome), "", false, deliveryErr
		}
		if _, err := s.handler.Store.DeferCapabilityExecutionForAuth(ctx, execution.ID, execution.LeaseToken); err != nil {
			return "", executionID, false, markDurableAgentStateError("defer capability for authentication", err)
		}
		if _, err := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse); err != nil {
			return "", executionID, false, err
		}
		return "", executionID, true, nil
	}
	_, deliveryErr := deliverCapabilityPresentation(ctx, ident, presentation, sendResponse)
	text := encodeCapabilityOutcome(string(invocation.ID()), outcome)
	if deliveryErr != nil {
		if tracked {
			if capabilityOutcomeIsUnknown(outcome) {
				if _, finishErr := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, text, "capability returned an unknown outcome"); finishErr != nil {
					return "", executionID, false, markDurableAgentStateError("record unknown capability outcome", finishErr)
				}
			} else {
				if _, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", deliveryErr); finishErr != nil {
					return "", executionID, false, markDurableAgentStateError("record capability delivery failure", finishErr)
				}
			}
		}
		return "", executionID, false, deliveryErr
	}
	if tracked {
		if capabilityOutcomeIsUnknown(outcome) {
			if _, err := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, text, "capability returned an unknown outcome"); err != nil {
				return "", executionID, false, markDurableAgentStateError("record unknown capability outcome", err)
			}
			return text, executionID, false, nil
		}
		var outcomeErr error
		if outcome.Status != commands.CapabilityOutcomeSuccess {
			outcomeErr = capabilityExecutionDiagnostic(outcome.Status)
		}
		if _, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, text, outcomeErr); err != nil {
			return "", executionID, false, markDurableAgentStateError("finish capability execution", err)
		}
	}
	return text, executionID, false, nil
}

func (s *Service) resolveHostCapability(
	ctx context.Context,
	state capabilityInterruptState,
	ident store.Identity,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
	persistResult bool,
) (string, error) {
	if s.handler.Store == nil {
		return "", errors.New("capability execution store is unavailable")
	}
	toolCallID := strings.TrimSpace(state.ToolCallID)
	if toolCallID == "" {
		return "", errors.New("capability execution state has no tool call id")
	}
	jobID, err := capabilityJobIDForExecutions(ctx, s.handler.Store, state.ExecutionIDs, ident)
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
			return "", markDurableAgentStateError("read capability execution", err)
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
				return "", markDurableAgentStateError("claim approved capability", err)
			}
			if !execute {
				if claimed.State == store.CapabilityExecutionRunning && !capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
					current, marked, err := markStaleCapabilityExecutionUnknown(ctx, s.handler.Store, claimed)
					if err != nil {
						return "", err
					}
					if !marked && !capabilityExecutionTerminal(current.State) {
						return "", errors.New("capability execution ownership changed while marking an interrupted mutation")
					}
					execution = current
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
				claimed, execute, claimErr := s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, store.ConversationJobLeaseFromContext(ctx, execution.JobID))
				if claimErr != nil {
					return "", markDurableAgentStateError("recover approved capability read", claimErr)
				}
				if !execute {
					if capabilityExecutionTerminal(claimed.State) {
						execution = claimed
						break
					}
					return "", errors.New("capability read ownership changed before recovery")
				}
				var waiting bool
				execution, waiting, err = s.executeApprovedCapability(ctx, claimed, ident, sendResponse)
				if err != nil {
					return "", err
				}
				authWait = authWait || waiting
			} else if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
				current, marked, markErr := markStaleCapabilityExecutionUnknown(ctx, s.handler.Store, execution)
				if markErr != nil {
					return "", markErr
				}
				if !marked && !capabilityExecutionTerminal(current.State) {
					return "", errors.New("capability execution ownership changed while marking an interrupted mutation")
				}
				execution = current
			} else {
				return "", fmt.Errorf("capability execution %s is still running", execution.ID)
			}
		}
		if !capabilityExecutionTerminal(execution.State) && execution.State != store.CapabilityExecutionWaitingAuth {
			return "", fmt.Errorf("capability execution %s is not terminal: %s", execution.ID, execution.State)
		}
		if execution.State != store.CapabilityExecutionWaitingAuth {
			if execution.State != store.CapabilityExecutionSucceeded {
				toolOutcomesFromContext(ctx).markError(toolCallID)
			}
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
	result := joinCapabilityResults(results)
	if persistResult {
		if err := s.persistResumedCapabilityResult(ctx, ident, jobID, toolCallID, result); err != nil {
			return "", err
		}
	}
	return result, nil
}

func (s *Service) persistResumedCapabilityResult(
	ctx context.Context,
	ident store.Identity,
	jobID int64,
	toolCallID string,
	result string,
) error {
	return s.persistResumedToolResult(ctx, ident, jobID, toolCallID, capabilityToolName, result)
}

func (s *Service) persistResumedToolResult(
	ctx context.Context,
	ident store.Identity,
	jobID int64,
	toolCallID string,
	toolName string,
	result string,
) error {
	if s.handler.Store == nil || jobID <= 0 {
		return nil
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return errors.New("resumed capability result has no tool call id")
	}
	job, err := s.handler.Store.GetConversationJob(ctx, jobID)
	if err != nil {
		return markDurableAgentStateError("read resumed capability job", err)
	}
	if job == nil {
		return markDurableAgentStateError("read resumed capability job", errors.New("conversation job is missing"))
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return errors.New("resumed tool result has no tool name")
	}
	_, _, err = s.handler.Store.AppendConversationEvent(ctx, store.ConversationEvent{
		Identity: ident, JobID: jobID, JobRevision: job.Revision, JobLeaseToken: job.LeaseToken,
		DedupeKey: agentToolResultDedupeKey(jobID, toolCallID),
		Type:      s.toolEventType(ctx, jobID, toolCallID), Content: result,
		ToolCallID: toolCallID, ToolName: toolName,
	})
	return markDurableAgentStateError("persist resumed capability result", err)
}

func capabilityJobIDForExecutions(ctx context.Context, db *store.Store, ids []string, ident store.Identity) (int64, error) {
	var jobID int64
	for _, id := range ids {
		execution, found, err := db.CapabilityExecution(ctx, id)
		if err != nil {
			return 0, markDurableAgentStateError("locate capability job", err)
		}
		if !found {
			return 0, fmt.Errorf("capability execution %s is missing", id)
		}
		if execution.Identity != ident {
			return 0, fmt.Errorf("capability execution %s belongs to another conversation", id)
		}
		if jobID == 0 {
			jobID = execution.JobID
			continue
		}
		if execution.JobID != jobID {
			return 0, errors.New("capability execution batch spans multiple conversation jobs")
		}
	}
	if jobID == 0 {
		return 0, errors.New("capability execution batch has no persisted job")
	}
	return jobID, nil
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
		return ctx, markDurableAgentStateError("read capability job lease", err)
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
	return current != "" && owner != current
}

func markStaleCapabilityExecutionUnknown(ctx context.Context, db *store.Store, execution store.CapabilityExecution) (store.CapabilityExecution, bool, error) {
	if db == nil {
		return store.CapabilityExecution{}, false, errors.New("capability execution store is unavailable")
	}
	current, marked, err := db.MarkStaleCapabilityExecutionUnknown(ctx, execution.ID, execution.JobID,
		store.ConversationJobLeaseFromContext(ctx, execution.JobID),
		"进程中断，外部操作结果未知；系统没有自动重试")
	return current, marked, markDurableAgentStateError("record interrupted mutation outcome", err)
}

func capabilityOutcomeIsUnknown(outcome commands.CapabilityOutcome) bool {
	return outcome.Status == commands.CapabilityOutcomeUnknown
}

func capabilityOutcomeIsToolError(status commands.CapabilityOutcomeStatus) bool {
	switch status {
	case commands.CapabilityOutcomeFailed,
		commands.CapabilityOutcomeUnknown,
		commands.CapabilityOutcomeInvalidInput,
		commands.CapabilityOutcomeForbidden,
		commands.CapabilityOutcomeNotFound:
		return true
	default:
		return false
	}
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
		finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", runErr)
		return finished, false, markDurableAgentStateError("record invalid confirmed capability", err)
	}
	outcome, err := s.handler.ExecuteCapability(ctx, commands.Input{Identity: ident, SuppressLog: true, Origin: commands.InvocationOriginAgent}, invocation.ID(), invocation.Args)
	if err != nil {
		finished, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", err)
		return finished, false, markDurableAgentStateError("record approved capability failure", finishErr)
	}
	if capabilityOutcomeIsToolError(outcome.Status) {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
	}
	presentation := s.handler.PresentCapabilityOutcome(invocation, outcome)
	text := encodeCapabilityOutcome(string(invocation.ID()), outcome)
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		deferred, err := s.handler.Store.DeferCapabilityExecutionForAuth(ctx, execution.ID, execution.LeaseToken)
		if err != nil {
			return deferred, false, markDurableAgentStateError("defer approved capability for authentication", err)
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
			finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", runErr)
			return finished, false, markDurableAgentStateError("record missing host response sender", err)
		}
		if err := sendResponse(ctx, ident, presentation.Response); err != nil {
			if capabilityOutcomeIsUnknown(outcome) {
				finished, finishErr := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, text, "capability returned an unknown outcome")
				return finished, false, markDurableAgentStateError("record unknown approved capability outcome", finishErr)
			}
			finished, finishErr := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", err)
			return finished, false, markDurableAgentStateError("record approved capability delivery failure", finishErr)
		}
	}
	if capabilityOutcomeIsUnknown(outcome) {
		finished, err := s.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, text, "capability returned an unknown outcome")
		return finished, false, markDurableAgentStateError("record unknown approved capability outcome", err)
	}
	var outcomeErr error
	if outcome.Status != commands.CapabilityOutcomeSuccess {
		outcomeErr = capabilityExecutionDiagnostic(outcome.Status)
	}
	finished, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, text, outcomeErr)
	return finished, false, markDurableAgentStateError("finish approved capability execution", err)
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
	return text, nil
}

func encodeCapabilityOutcome(operation string, outcome commands.CapabilityOutcome) string {
	var err error
	if outcome.Status != commands.CapabilityOutcomeSuccess {
		err = errors.New(outcome.Response.Text)
	}
	return toolresult.Encode("bot", operation, string(outcome.Status), time.Now(), outcome.Response.Data, err)
}

func capabilityExecutionModelResult(execution store.CapabilityExecution) string {
	if strings.TrimSpace(execution.Result) != "" {
		return execution.Result
	}
	var err error
	if execution.State != store.CapabilityExecutionSucceeded {
		message := execution.Error
		switch execution.State {
		case store.CapabilityExecutionDenied:
			message = "用户拒绝了该操作，未执行任何变更。"
		case store.CapabilityExecutionUnknown:
			message = "操作结果未知，可能已经执行；不得自动重试。"
		case store.CapabilityExecutionCancelled:
			message = "操作已取消，未执行任何变更。"
		case store.CapabilityExecutionExpired:
			message = "操作已过期，未执行任何变更。"
		}
		if message == "" {
			message = "操作未成功完成。"
		}
		err = errors.New(message)
	}
	return toolresult.Encode("bot", execution.Capability, string(execution.State), executionObservedAt(execution), nil, err)
}

func executionObservedAt(execution store.CapabilityExecution) time.Time {
	if execution.FinishedAt != nil {
		return *execution.FinishedAt
	}
	if execution.ConfirmedAt != nil {
		return *execution.ConfirmedAt
	}
	return execution.CreatedAt
}

func joinCapabilityResults(results []string) string {
	if len(results) == 1 {
		return results[0]
	}
	values := make([]any, 0, len(results))
	for _, result := range results {
		values = append(values, toolresult.Data(result))
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
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
