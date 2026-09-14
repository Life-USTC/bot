package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func (s *lazyMCPSession) call(ctx context.Context, input campusToolCallInput) (string, error) {
	if wasInterrupted, hasState, state := tool.GetInterruptState[capabilityInterruptState](ctx); wasInterrupted {
		if !hasState || len(state.ExecutionIDs) != 1 || strings.TrimSpace(state.ToolCallID) == "" {
			return "", errors.New("confirmed campus tool checkpoint has no operation state")
		}
		return s.resolveCampusExecution(ctx, state, true)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "", botmcp.NewRecoverableToolError("call_campus_tool", "name is required")
	}
	if existing, err := s.existingCampusExecution(ctx, name, input.Arguments); err != nil {
		return "", err
	} else if existing != nil {
		switch existing.State {
		case store.CapabilityExecutionAwaitingConfirmation:
			state := capabilityInterruptState{ExecutionIDs: []string{existing.ID}, ToolCallID: existing.ToolCallID}
			return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
				Kind: capabilityInterruptConfirmation, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
			}, state)
		case store.CapabilityExecutionSucceeded, store.CapabilityExecutionFailed,
			store.CapabilityExecutionDenied, store.CapabilityExecutionUnknown,
			store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
			return campusExecutionModelResult(*existing), nil
		case store.CapabilityExecutionRunning:
			if !capabilityExecutionIsRead(*existing) && capabilityExecutionHasStaleLease(ctx, *existing) {
				current, marked, markErr := markStaleCapabilityExecutionUnknown(ctx, s.service.handler.Store, *existing)
				if markErr != nil {
					return "", markErr
				}
				if marked || capabilityExecutionTerminal(current.State) {
					return campusExecutionModelResult(current), nil
				}
				return "", errors.New("campus tool ownership changed while marking an interrupted mutation")
			}
		}
	}
	if err := s.ensure(ctx); err != nil {
		return "", normalizeCampusReadCallError(ctx, name, err)
	}
	if _, found := s.tools[name]; !found {
		return "", botmcp.NewRecoverableToolError(name, "the requested campus tool was not found in the current tools/list")
	}

	execution, tracked, execute, err := s.prepareExecution(ctx, name, input.Arguments)
	if err != nil {
		return "", err
	}
	if !tracked {
		if campusEffectOf(s.tools[name]) != campusEffectRead {
			return "", errors.New("durable confirmation requires a persisted conversation job")
		}
		return s.invokeCampusRead(ctx, name, input.Arguments)
	}

	if execution.State == store.CapabilityExecutionAwaitingConfirmation {
		state := capabilityInterruptState{ExecutionIDs: []string{execution.ID}, ToolCallID: execution.ToolCallID}
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptConfirmation, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
		}, state)
	}
	if capabilityExecutionTerminal(execution.State) {
		return campusExecutionModelResult(execution), nil
	}
	if execution.State == store.CapabilityExecutionApproved || execution.State == store.CapabilityExecutionWaitingAuth {
		lease := store.ConversationJobLeaseFromContext(ctx, execution.JobID)
		claimed, canExecute, claimErr := s.service.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, lease)
		if claimErr != nil {
			return "", markDurableAgentStateError("claim approved campus tool", claimErr)
		}
		if !canExecute {
			if capabilityExecutionTerminal(claimed.State) {
				return campusExecutionModelResult(claimed), nil
			}
			if claimed.State == store.CapabilityExecutionRunning && !capabilityExecutionIsRead(claimed) && capabilityExecutionHasStaleLease(ctx, claimed) {
				current, marked, markErr := markStaleCapabilityExecutionUnknown(ctx, s.service.handler.Store, claimed)
				if markErr != nil {
					return "", markErr
				}
				if !marked && !capabilityExecutionTerminal(current.State) {
					return "", errors.New("campus tool ownership changed while marking an interrupted mutation")
				}
				return campusExecutionModelResult(current), nil
			}
			return "", fmt.Errorf("campus tool %s claim was lost while state is %s", execution.ID, claimed.State)
		}
		execution = claimed
		execute = true
	}
	if execution.State == store.CapabilityExecutionRunning && !execute {
		if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
			claimed, canExecute, claimErr := s.service.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, store.ConversationJobLeaseFromContext(ctx, execution.JobID))
			if claimErr != nil {
				return "", markDurableAgentStateError("recover campus read", claimErr)
			}
			if !canExecute {
				if capabilityExecutionTerminal(claimed.State) {
					return campusExecutionModelResult(claimed), nil
				}
				return "", errors.New("campus read ownership changed before recovery")
			}
			execution = claimed
			execute = true
		} else if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
			current, marked, markErr := markStaleCapabilityExecutionUnknown(ctx, s.service.handler.Store, execution)
			if markErr != nil {
				return "", markErr
			}
			if !marked && !capabilityExecutionTerminal(current.State) {
				return "", errors.New("campus tool ownership changed while marking an interrupted mutation")
			}
			return campusExecutionModelResult(current), nil
		} else {
			return "", errors.New("campus tool is already running under the current job lease")
		}
	}
	if execution.State != store.CapabilityExecutionRunning || !execute {
		return campusExecutionModelResult(execution), nil
	}
	_, finished, callErr := s.executeApprovedCampusCall(ctx, execution)
	if isDurableAgentStateError(callErr) {
		return "", callErr
	}
	if callErr != nil {
		if isCampusReadControlError(ctx, callErr) {
			return "", callErr
		}
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
	}
	return campusExecutionModelResult(finished), nil
}

func (s *lazyMCPSession) invokeCampusRead(ctx context.Context, name string, arguments map[string]any) (string, error) {
	result, callErr := s.session.Call(ctx, name, arguments)
	if callErr != nil {
		return "", normalizeCampusReadCallError(ctx, name, callErr)
	}
	if name == "catalog_rooms_map" {
		if err := s.deliverRoomMapResponse(ctx, result); err != nil {
			return "", err
		}
	}
	return toolresult.Encode("mcp", name, "succeeded", time.Now(), toolresult.Data(result), nil), nil
}

// isCampusReadControlError identifies errors that belong to the agent or
// coordinator rather than the remote MCP operation. They must reach the run
// controller so cancellation, budgets, and persistence failures keep their
// existing handling.
func isCampusReadControlError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// An http.Client timeout can wrap DeadlineExceeded while the run
		// context remains live. That single remote request is recoverable;
		// only an ended run context is a control failure.
		if ctx == nil || ctx.Err() == nil {
			return false
		}
		return true
	}
	return isDurableAgentStateError(err) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, errAgentRunDeadline) ||
		errors.Is(err, errAgentContextBudget) ||
		errors.Is(err, errAgentRunTokenBudget) ||
		errors.Is(err, errAgentModelAttemptBudget) ||
		errors.Is(err, errAgentToolCallBudget) ||
		errors.Is(err, errAgentNonProgress) ||
		errors.Is(err, errRepeatedToolCall) ||
		errors.Is(err, errLLMUpstreamCanceled)
}

// normalizeCampusReadCallError turns an ordinary MCP transport/server error
// into the recoverable tool error consumed by service.go's result middleware.
// The fixed detail deliberately omits URLs, response bodies, and credentials.
func normalizeCampusReadCallError(ctx context.Context, name string, err error) error {
	return normalizeCampusExternalError(ctx, name, err, "校园数据读取暂时失败，可能是网络或服务暂时不可用；请改用其他校园工具或稍后重试。")
}

func normalizeCampusSetupError(ctx context.Context, name string, err error) error {
	return normalizeCampusExternalError(ctx, name, err, "校园工具暂时不可用，可能是网络或服务暂时不可用；请改用 Bot 命令或稍后重试。")
}

func normalizeCampusExternalError(ctx context.Context, name string, err error, unavailableDetail string) error {
	if err == nil || isCampusReadControlError(ctx, err) {
		return err
	}
	if isMCPAuthorizationError(err) {
		return botmcp.NewRecoverableToolError(name, "校园服务登录权限已失效，请先登录或重新登录后再试。")
	}
	if detail, ok := botmcp.ModelToolErrorResult(err); ok {
		return botmcp.NewRecoverableToolError(name, detail)
	}
	return botmcp.NewRecoverableToolError(name, unavailableDetail)
}

func campusArgumentsForRemote(name string, arguments map[string]any, approved bool) map[string]any {
	result := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		result[key] = value
	}
	if name == "graphql_operation_run" {
		// The server requires this marker for mutations. Model input is never
		// allowed to authorize itself; only the approved execution path sends
		// true to the remote server.
		result["confirmed"] = approved
	}
	return result
}

func (s *lazyMCPSession) resolveCampusExecution(ctx context.Context, state capabilityInterruptState, persistResult bool) (string, error) {
	if s.service == nil || s.service.handler.Store == nil {
		return "", errors.New("campus tool execution store is unavailable")
	}
	if len(state.ExecutionIDs) != 1 || strings.TrimSpace(state.ToolCallID) == "" {
		return "", errors.New("campus tool execution state is incomplete")
	}
	jobID, err := capabilityJobIDForExecutions(ctx, s.service.handler.Store, state.ExecutionIDs, s.identity)
	if err != nil {
		return "", err
	}
	if jobID != s.jobID {
		return "", fmt.Errorf("campus tool checkpoint belongs to job %d, want job %d", jobID, s.jobID)
	}
	leaseCtx, err := ensureCapabilityJobLease(ctx, s.service.handler.Store, jobID)
	if err != nil {
		return "", err
	}
	ctx = leaseCtx
	execution, found, err := s.service.handler.Store.CapabilityExecution(ctx, state.ExecutionIDs[0])
	if err != nil {
		return "", markDurableAgentStateError("read campus tool execution", err)
	}
	if !found {
		return "", fmt.Errorf("campus tool execution %s is missing", state.ExecutionIDs[0])
	}
	if strings.TrimSpace(execution.ToolCallID) != strings.TrimSpace(state.ToolCallID) {
		return "", errors.New("campus tool checkpoint belongs to another tool call")
	}
	if _, err := campusExecutionToolName(execution); err != nil {
		return "", err
	}

	switch execution.State {
	case store.CapabilityExecutionAwaitingConfirmation:
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptConfirmation, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
		}, state)
	case store.CapabilityExecutionApproved, store.CapabilityExecutionWaitingAuth:
		claimed, canExecute, claimErr := s.service.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, store.ConversationJobLeaseFromContext(ctx, execution.JobID))
		if claimErr != nil {
			return "", markDurableAgentStateError("claim approved campus tool", claimErr)
		}
		if !canExecute {
			if claimed.State == store.CapabilityExecutionWaitingAuth {
				return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
					Kind: capabilityInterruptAuth, ExecutionIDs: append([]string(nil), state.ExecutionIDs...),
				}, state)
			}
			if !capabilityExecutionTerminal(claimed.State) {
				return "", fmt.Errorf("campus tool execution %s claim was lost while state is %s", claimed.ID, claimed.State)
			}
			execution = claimed
		} else {
			execution = claimed
		}
	case store.CapabilityExecutionRunning:
		if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
			claimed, canExecute, claimErr := s.service.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, store.ConversationJobLeaseFromContext(ctx, execution.JobID))
			if claimErr != nil {
				return "", markDurableAgentStateError("recover campus read", claimErr)
			}
			if canExecute {
				execution = claimed
			} else if capabilityExecutionTerminal(claimed.State) {
				execution = claimed
			} else {
				return "", errors.New("campus read ownership changed before recovery")
			}
		} else if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(ctx, execution) {
			current, marked, markErr := markStaleCapabilityExecutionUnknown(ctx, s.service.handler.Store, execution)
			if markErr != nil {
				return "", markErr
			}
			if !marked && !capabilityExecutionTerminal(current.State) {
				return "", errors.New("campus tool ownership changed while marking an interrupted mutation")
			}
			execution = current
		} else {
			return "", fmt.Errorf("campus tool execution %s is still running", execution.ID)
		}
	}

	if execution.State == store.CapabilityExecutionRunning {
		var callErr error
		_, execution, callErr = s.executeApprovedCampusCall(ctx, execution)
		if callErr != nil && (isDurableAgentStateError(callErr) || isCampusReadControlError(ctx, callErr)) {
			return "", callErr
		}
	}
	if !capabilityExecutionTerminal(execution.State) {
		return "", fmt.Errorf("campus tool execution %s is not terminal: %s", execution.ID, execution.State)
	}
	result := campusExecutionModelResult(execution)
	if execution.State != store.CapabilityExecutionSucceeded {
		toolOutcomesFromContext(ctx).markError(state.ToolCallID)
	}
	if persistResult {
		if err := s.service.persistResumedToolResult(ctx, s.identity, jobID, state.ToolCallID, campusCallToolName, result); err != nil {
			return "", err
		}
	}
	return result, nil
}

func campusExecutionToolName(execution store.CapabilityExecution) (string, error) {
	capability := strings.TrimSpace(execution.Capability)
	if !strings.HasPrefix(capability, "mcp:") {
		return "", fmt.Errorf("campus tool execution %s has an invalid capability", execution.ID)
	}
	name := strings.TrimSpace(strings.TrimPrefix(capability, "mcp:"))
	if name == "" {
		return "", fmt.Errorf("campus tool execution %s has an empty tool name", execution.ID)
	}
	return name, nil
}

func (s *lazyMCPSession) existingCampusExecution(ctx context.Context, name string, arguments map[string]any) (*store.CapabilityExecution, error) {
	if s == nil || s.service == nil || s.service.handler.Store == nil || s.jobID <= 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(campusArgumentsForRemote(name, arguments, false))
	if err != nil {
		return nil, err
	}
	callID := campusToolCallID(ctx, s.jobID, name, encoded)
	executions, err := s.service.handler.Store.CapabilityExecutionsForJob(ctx, s.jobID)
	if err != nil {
		return nil, markDurableAgentStateError("read existing campus execution", err)
	}
	capability := "mcp:" + name
	for index := range executions {
		execution := &executions[index]
		if execution.Identity != s.identity {
			return nil, errors.New("existing campus execution belongs to another conversation")
		}
		if execution.ToolCallID != callID || execution.Capability != capability || len(execution.Arguments) != 1 || execution.Arguments[0] != string(encoded) {
			continue
		}
		return execution, nil
	}
	return nil, nil
}

func campusExecutionModelResult(execution store.CapabilityExecution) string {
	name := strings.TrimPrefix(execution.Capability, "mcp:")
	var err error
	var data any
	if execution.State == store.CapabilityExecutionSucceeded {
		if execution.Result != "" {
			data = toolresult.Data(execution.Result)
		}
	} else {
		detail := execution.Error
		switch execution.State {
		case store.CapabilityExecutionDenied:
			detail = "用户拒绝了该操作，未执行任何变更。"
		case store.CapabilityExecutionUnknown:
			detail = "操作结果未知，可能已经执行；不得自动重试。"
		case store.CapabilityExecutionCancelled:
			detail = "操作已取消，未执行任何变更。"
		case store.CapabilityExecutionExpired:
			detail = "操作已过期，未执行任何变更。"
		}
		if detail == "" {
			detail = "校园操作未成功完成。"
		}
		err = errors.New(textutil.SafeLogText(detail))
	}
	return toolresult.Encode("mcp", name, string(execution.State), executionObservedAt(execution), data, err)
}

func (s *lazyMCPSession) executeApprovedCampusCall(ctx context.Context, execution store.CapabilityExecution) (string, store.CapabilityExecution, error) {
	persistCtx := context.WithoutCancel(ctx)
	name, err := campusExecutionToolName(execution)
	if err != nil {
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "校园工具无法恢复已确认的参数。", err)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record invalid confirmed campus tool", finishErr)
		}
		return finished.Result, finished, err
	}
	if err := s.ensure(ctx); err != nil {
		err = normalizeCampusSetupError(ctx, name, err)
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", err)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record campus tool setup failure", finishErr)
		}
		return finished.Result, finished, err
	}
	candidate, found := s.tools[name]
	if !found {
		setupErr := fmt.Errorf("campus tool %s is no longer available in the current tools/list", name)
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", setupErr)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record unavailable campus tool", finishErr)
		}
		return finished.Result, finished, setupErr
	}
	if strings.TrimSpace(execution.Effect) != string(campusEffectOf(candidate)) {
		setupErr := fmt.Errorf("campus tool %s effect changed after confirmation", name)
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", setupErr)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record changed campus tool effect", finishErr)
		}
		return finished.Result, finished, setupErr
	}
	if len(execution.Arguments) != 1 {
		setupErr := errors.New("campus tool execution has invalid persisted arguments")
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", setupErr)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record invalid campus tool arguments", finishErr)
		}
		return finished.Result, finished, setupErr
	}
	arguments := make(map[string]any)
	if err := json.Unmarshal([]byte(execution.Arguments[0]), &arguments); err != nil {
		setupErr := fmt.Errorf("decode persisted campus tool arguments: %w", err)
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", setupErr)
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record invalid campus tool arguments", finishErr)
		}
		return finished.Result, finished, setupErr
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	arguments = campusArgumentsForRemote(name, arguments, true)
	result, callErr := s.session.Call(ctx, name, arguments)
	if callErr == nil && name == "catalog_rooms_map" {
		if deliveryErr := s.deliverRoomMapResponse(ctx, result); deliveryErr != nil {
			callErr = deliveryErr
		}
	}
	if safe, ok := botmcp.ModelToolErrorResult(callErr); ok {
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, safe, errors.New(safe))
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("finish campus tool failure", finishErr)
		}
		return safe, finished, callErr
	}
	if callErr != nil {
		if capabilityExecutionIsRead(execution) {
			callErr = normalizeCampusReadCallError(ctx, name, callErr)
			finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, "", callErr)
			if finishErr != nil {
				return "", finished, markDurableAgentStateError("finish campus read failure", finishErr)
			}
			return finished.Result, finished, callErr
		}
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecutionUnknown(persistCtx, execution.ID, execution.LeaseToken, "", "MCP transport failed; result is unknown and was not retried")
		if finishErr != nil {
			return "", finished, markDurableAgentStateError("record unknown campus tool outcome", finishErr)
		}
		return finished.Result, finished, callErr
	}
	if capabilityExecutionIsRead(execution) {
		receipt := execution.Receipt
		receipt.Subject = campusReceiptSubject(name, arguments, result)
		if receipt.Subject != execution.Receipt.Subject {
			if receiptErr := s.service.handler.Store.UpdateCapabilityExecutionReceipt(persistCtx, execution.ID, execution.LeaseToken, receipt); receiptErr != nil {
				return "", execution, markDurableAgentStateError("update campus read receipt", receiptErr)
			}
		}
	}
	finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(persistCtx, execution.ID, execution.LeaseToken, result, nil)
	if finishErr != nil {
		return "", finished, markDurableAgentStateError("finish campus tool execution", finishErr)
	}
	return result, finished, nil
}

func campusReceiptResource(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "young_event"):
		return "第二课堂活动"
	case strings.Contains(lower, "course"), strings.Contains(lower, "section"), strings.Contains(lower, "class"):
		return "课程"
	case strings.Contains(lower, "teacher"):
		return "教师"
	case strings.Contains(lower, "semester"):
		return "学期"
	case strings.Contains(lower, "homework"):
		return "作业"
	case strings.Contains(lower, "exam"):
		return "考试"
	case strings.Contains(lower, "bus"):
		return "校车"
	case strings.Contains(lower, "weather"), strings.Contains(lower, "forecast"):
		return "天气"
	case strings.Contains(lower, "graphql"), strings.Contains(lower, "operation"):
		return "校园数据操作"
	default:
		return "校园数据"
	}
}

func campusReceiptSubject(name string, arguments map[string]any, result string) string {
	if subject := campusResultSubject(result); subject != "" {
		return subject
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(arguments[key]))
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) > 0 {
		return strings.Join(values, " ")
	}
	return name
}

func (s *lazyMCPSession) prepareExecution(ctx context.Context, name string, arguments map[string]any) (store.CapabilityExecution, bool, bool, error) {
	candidate, found := s.tools[name]
	if !found {
		if err := s.ensure(ctx); err != nil {
			return store.CapabilityExecution{}, false, false, err
		}
		candidate, found = s.tools[name]
		if !found {
			return store.CapabilityExecution{}, false, false, fmt.Errorf("campus tool %s was not found in the current tools/list", name)
		}
	}
	effect := campusEffectOf(candidate)
	if s.service == nil || s.service.handler.Store == nil || s.jobID <= 0 {
		if effect != campusEffectRead {
			return store.CapabilityExecution{}, false, false, errors.New("durable confirmation requires a persisted conversation job")
		}
		return store.CapabilityExecution{}, false, true, nil
	}
	leaseCtx, err := ensureCapabilityJobLease(ctx, s.service.handler.Store, s.jobID)
	if err != nil {
		return store.CapabilityExecution{}, false, false, err
	}
	ctx = leaseCtx
	arguments = campusArgumentsForRemote(name, arguments, false)
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return store.CapabilityExecution{}, false, false, err
	}
	callID := campusToolCallID(ctx, s.jobID, name, encoded)
	receipt := store.CapabilityReceipt{
		Action: campusReceiptAction(effect), Resource: campusReceiptResource(name),
		Subject: campusReceiptSubject(name, arguments, ""),
	}
	if effect != campusEffectRead {
		receipt.Subject = campusReceiptSubjectWithArguments(name, arguments)
	}
	execution, created, err := s.service.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: s.identity, JobID: s.jobID, LeaseToken: store.ConversationJobLeaseFromContext(ctx, s.jobID),
		DedupeKey:  "conversation-job:" + fmt.Sprint(s.jobID) + ":mcp:" + callID,
		ToolCallID: callID, Capability: "mcp:" + name, Arguments: []string{string(encoded)}, Effect: string(effect),
		Receipt: receipt, RequiresConfirmation: effect == campusEffectDestructive,
	})
	return execution, true, created && execution.State == store.CapabilityExecutionRunning, markDurableAgentStateError("prepare campus tool execution", err)
}

func campusToolCallID(ctx context.Context, jobID int64, name string, encoded []byte) string {
	if callID := strings.TrimSpace(compose.GetToolCallID(ctx)); callID != "" {
		return callID
	}
	digest := sha256.Sum256(append([]byte(name+"\x00"), encoded...))
	return fmt.Sprintf("%x", digest[:12])
}

func campusReceiptAction(effect campusToolEffect) string {
	switch effect {
	case campusEffectRead:
		return "查询"
	case campusEffectWrite:
		return "修改"
	default:
		return "危险操作"
	}
}

func campusReceiptSubjectWithArguments(name string, arguments map[string]any) string {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return name
	}
	return name + " 参数=" + string(encoded)
}
