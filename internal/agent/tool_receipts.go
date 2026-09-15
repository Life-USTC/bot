package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudwego/eino/compose"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

// Domain calls already own durable execution records. This middleware adds
// receipts for completed discovery, resource, prompt and utility calls, and
// for validation/setup failures that never reached domain execution. It uses
// the same ledger and output commit as domain receipts, including on resume.
func (s *Service) toolReceiptMiddleware(identity store.Identity, jobID int64) compose.InvokableToolMiddleware {
	return func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			out, err := next(ctx, input)
			// Interrupts have their own saved operation. An incomplete tool call
			// is not a completed query and must not acquire a success receipt.
			if err != nil || out == nil || input == nil || jobID <= 0 || s.handler.Store == nil {
				return out, err
			}
			if err := s.saveToolReceipt(ctx, identity, jobID, input, out.Result); err != nil {
				return nil, markDurableAgentStateError("save tool receipt", err)
			}
			return out, nil
		}
	}
}

func (s *Service) saveToolReceipt(ctx context.Context, identity store.Identity, jobID int64, input *compose.ToolInput, result string) error {
	callID := strings.TrimSpace(input.CallID)
	if callID == "" {
		return errors.New("tool receipt has no call ID")
	}
	dedupeKey := fmt.Sprintf("conversation-job:%d:tool-receipt:%s", jobID, callID)
	capability := "tool:" + input.Name
	arguments := []string{input.Arguments}
	if input.Name == campusCallToolName {
		var call campusToolCallInput
		if json.Unmarshal([]byte(input.Arguments), &call) == nil && strings.TrimSpace(call.Name) != "" {
			capability = "mcp:" + strings.TrimSpace(call.Name)
			encoded, err := json.Marshal(campusArgumentsForRemote(call.Name, call.Arguments, false))
			if err != nil {
				return err
			}
			arguments = []string{string(encoded)}
		}
	} else if input.Name == "run_bot_command" {
		var command botCommandInput
		if json.Unmarshal([]byte(input.Arguments), &command) == nil && !commands.HasAdditionalCommandLine(command.Command) {
			parsed := commands.ParseCommand(command.Command)
			if parsed.Recognized() {
				capability = string(parsed.Invocation.ID())
				arguments = parsed.Invocation.Args
			}
		}
	}
	executions, err := s.handler.Store.CapabilityExecutionsForJob(ctx, jobID)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		if execution.ToolCallID == callID {
			if execution.DedupeKey == dedupeKey && execution.State == store.CapabilityExecutionRunning {
				break // Complete a receipt interrupted between prepare and finish.
			}
			return nil
		}
		// A saved mutation can be returned under a new model call ID without
		// executing again. Preserve its existing receipt and confirmation.
		if execution.Effect != "read" && execution.Capability == capability && slices.Equal(execution.Arguments, arguments) {
			return nil
		}
	}
	var envelope toolresult.Result
	if !toolresult.IsEncoded(result) || json.Unmarshal([]byte(result), &envelope) != nil {
		return errors.New("tool receipt requires a structured result")
	}
	var outcomeErr error
	if envelope.Status != "succeeded" {
		outcomeErr = fmt.Errorf("tool outcome: %s", envelope.Status)
	}
	lease := store.ConversationJobLeaseFromContext(ctx, jobID)
	execution, created, err := s.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: identity, JobID: jobID, LeaseToken: lease,
		DedupeKey:  dedupeKey,
		ToolCallID: callID, Capability: capability, Arguments: arguments, Effect: "read",
	})
	if err != nil || (!created && capabilityExecutionTerminal(execution.State)) {
		return err
	}
	if execution.LeaseToken != lease {
		var claimed bool
		execution, claimed, err = s.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, jobID, lease)
		if err != nil {
			return err
		}
		if !claimed {
			return errors.New("tool receipt recovery lost its lease")
		}
	}
	_, err = s.handler.Store.FinishCapabilityExecution(ctx, execution.ID, lease, result, outcomeErr)
	return err
}
