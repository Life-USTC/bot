package agent

import (
	"context"
	"encoding/gob"
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
	ExecutionIDs []string
}

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
		result, err := s.handler.ExecuteCapabilityForAgent(ctx, commands.Input{Identity: ident, SuppressLog: true}, id, input.Arguments)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(result.Text), nil
	}
	policy := invocation.Policy()
	if policy.Confirmation != commands.ConfirmUser {
		return s.executeUnconfirmedHostCapability(ctx, invocation, ident, jobID, compose.GetToolCallID(ctx), sendResponse)
	}
	if jobID <= 0 || s.handler.Store == nil {
		return "", errors.New("durable confirmation requires a persisted conversation job")
	}

	invocations := expandConfirmationInvocations(invocation)
	callID := strings.TrimSpace(compose.GetToolCallID(ctx))
	if callID == "" {
		callID = fmt.Sprintf("capability-%d", jobID)
	}
	state := capabilityInterruptState{ExecutionIDs: make([]string, 0, len(invocations))}
	for index, item := range invocations {
		receipt := receiptForInvocation(item)
		execution, _, err := s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID, Sequence: index,
			DedupeKey:  fmt.Sprintf("conversation-job:%d:tool:%s:operation:%d", jobID, callID, index),
			ToolCallID: callID, Capability: string(item.ID()), Arguments: append([]string(nil), item.Args...),
			Effect: string(item.Policy().Effect), Receipt: receipt, RequiresConfirmation: true,
		})
		if err != nil {
			return "", err
		}
		state.ExecutionIDs = append(state.ExecutionIDs, execution.ID)
	}
	return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{ExecutionIDs: append([]string(nil), state.ExecutionIDs...)}, state)
}

func (s *Service) executeUnconfirmedHostCapability(
	ctx context.Context,
	invocation commands.Invocation,
	ident store.Identity,
	jobID int64,
	toolCallID string,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (string, error) {
	var execution store.CapabilityExecution
	tracked := s.handler.Store != nil && jobID > 0
	if tracked {
		var err error
		execution, _, err = s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: jobID,
			DedupeKey:  fmt.Sprintf("conversation-job:%d:tool:%s", jobID, stableToolCallID(toolCallID, invocation)),
			ToolCallID: toolCallID, Capability: string(invocation.ID()), Arguments: append([]string(nil), invocation.Args...),
			Effect: string(invocation.Policy().Effect), Receipt: receiptForInvocation(invocation),
		})
		if err != nil {
			return "", err
		}
		if execution.State != store.CapabilityExecutionRunning {
			return capabilityExecutionModelResult(execution), nil
		}
	}
	result, err := s.handler.ExecuteCapabilityForAgent(ctx, commands.Input{Identity: ident, SuppressLog: true}, invocation.ID(), invocation.Args)
	if err != nil {
		if tracked {
			_, _ = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
		}
		return "", err
	}
	text, deliveryErr := deliverAgentCommandResult(ctx, ident, result, sendResponse)
	if deliveryErr != nil {
		if tracked {
			_, _ = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", deliveryErr)
		}
		return "", deliveryErr
	}
	if tracked {
		var outcomeErr error
		if !result.OK {
			outcomeErr = errors.New(strings.TrimSpace(text))
		}
		if _, err := s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, text, outcomeErr); err != nil {
			return "", err
		}
	}
	return text, nil
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
		case store.CapabilityExecutionApproved:
			claimed, execute, err := s.handler.Store.ClaimCapabilityExecution(ctx, execution.ID)
			if err != nil {
				return "", err
			}
			if !execute {
				execution = claimed
				break
			}
			execution, err = s.executeApprovedCapability(ctx, claimed, ident, sendResponse)
			if err != nil {
				return "", err
			}
		case store.CapabilityExecutionRunning:
			if err := s.handler.Store.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
				return "", err
			}
			execution, _, err = s.handler.Store.CapabilityExecution(ctx, execution.ID)
			if err != nil {
				return "", err
			}
		}
		results = append(results, capabilityExecutionModelResult(execution))
	}
	if pending {
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{ExecutionIDs: append([]string(nil), state.ExecutionIDs...)}, state)
	}
	return strings.TrimSpace(strings.Join(results, "\n\n")), nil
}

func (s *Service) executeApprovedCapability(
	ctx context.Context,
	execution store.CapabilityExecution,
	ident store.Identity,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (store.CapabilityExecution, error) {
	invocation, valid := commands.RestoreInvocation(commands.CapabilityID(execution.Capability), execution.Arguments)
	if !valid {
		runErr := errors.New("宿主无法恢复已确认的操作")
		return s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", runErr)
	}
	response, handled := s.handler.HandleInvocationResponse(ctx, commands.Input{
		Text: invocation.CanonicalCommand(), Identity: ident, SuppressLog: true,
	}, invocation)
	if !handled {
		runErr := errors.New("宿主无法执行已确认的操作")
		return s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", runErr)
	}
	presentation := invocation.Descriptor().Present(invocation, response, invocation.Policy())
	text := strings.TrimSpace(presentation.Text)
	if presentation.DeliveredByHost {
		if sendResponse == nil {
			runErr := errors.New("host response sender is unavailable")
			return s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", runErr)
		}
		if err := sendResponse(ctx, ident, presentation.Response); err != nil {
			return s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, "", err)
		}
		if text == "" {
			text = "结果已由宿主发送给用户。"
		}
	}
	return s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, text, nil)
}

func deliverAgentCommandResult(
	ctx context.Context,
	ident store.Identity,
	result commands.AgentCommandResult,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) (string, error) {
	text := strings.TrimSpace(result.Text)
	if !result.DeliveredByHost {
		return text, nil
	}
	if sendResponse == nil {
		return "", errors.New("host response sender is unavailable")
	}
	if err := sendResponse(ctx, ident, result.Response); err != nil {
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
		return "操作未执行：" + reason
	case store.CapabilityExecutionFailed:
		return "操作失败：" + strings.TrimSpace(execution.Error)
	case store.CapabilityExecutionUnknown, store.CapabilityExecutionRunning:
		return "操作结果未知：" + strings.TrimSpace(execution.Error)
	default:
		return "操作尚未执行。"
	}
}

func stableToolCallID(toolCallID string, invocation commands.Invocation) string {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID != "" {
		return toolCallID
	}
	return invocation.CanonicalCommand()
}

func expandConfirmationInvocations(invocation commands.Invocation) []commands.Invocation {
	if invocation.ID() != commands.CapabilitySubscription || len(invocation.Args) <= 2 || invocation.Args[0] != "import" {
		return []commands.Invocation{invocation}
	}
	result := make([]commands.Invocation, 0, len(invocation.Args)-1)
	for _, argument := range invocation.Args[1:] {
		item, ok := commands.NewInvocation(commands.CapabilitySubscription, []string{"import", argument})
		if ok {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return []commands.Invocation{invocation}
	}
	return result
}

func receiptForInvocation(invocation commands.Invocation) store.CapabilityReceipt {
	receipt := store.CapabilityReceipt{Subject: strings.TrimSpace(strings.Join(invocation.Args, " "))}
	switch invocation.ID() {
	case commands.CapabilityCourse, commands.CapabilityCourseSearch, commands.CapabilityCourseByJWID:
		receipt.Action, receipt.Resource = "查询", "课程"
	case commands.CapabilitySection, commands.CapabilitySectionSearch, commands.CapabilitySectionByJWID,
		commands.CapabilitySectionSchedules, commands.CapabilitySectionExams, commands.CapabilitySectionHomeworks:
		receipt.Action, receipt.Resource = "查询", "课程"
	case commands.CapabilityTeacher, commands.CapabilityTeacherSearch, commands.CapabilityTeacherByID:
		receipt.Action, receipt.Resource = "查询", "教师"
	case commands.CapabilitySemester, commands.CapabilityListSemesters:
		receipt.Action, receipt.Resource = "查询", "学期"
	case commands.CapabilitySubscription:
		if len(invocation.Args) > 1 && invocation.Args[0] == "import" {
			receipt.Action, receipt.Resource, receipt.Subject = "订阅", "课程", strings.Join(invocation.Args[1:], " ")
		} else {
			receipt.Action, receipt.Resource = "查询", "课程"
		}
	case commands.CapabilityUnsubscribeSectionByJWID:
		receipt.Action, receipt.Resource = "取消", "课程"
	default:
		if invocation.Policy().Confirmation == commands.ConfirmUser {
			receipt.Action, receipt.Resource, receipt.Subject = "执行", "操作", invocation.CanonicalCommand()
		}
	}
	if receipt.Subject == "" {
		receipt.Subject = invocation.CanonicalCommand()
	}
	return receipt
}
