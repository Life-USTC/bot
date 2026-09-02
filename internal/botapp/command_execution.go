package botapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

type responseCommitter func(context.Context, commands.Response, []string, store.ConversationJobTransition) error

func capabilityOutcomeIsUnknown(outcome commands.CapabilityOutcome) bool {
	return outcome.Status == commands.CapabilityOutcomeUnknown
}

func capabilityExecutionIsRead(execution store.CapabilityExecution) bool {
	return strings.EqualFold(strings.TrimSpace(execution.Effect), string(commands.EffectRead))
}

func capabilityExecutionHasStaleLease(execution store.CapabilityExecution, job store.ConversationJob) bool {
	owner := strings.TrimSpace(execution.LeaseToken)
	current := strings.TrimSpace(job.LeaseToken)
	return owner != current
}

func capabilityExecutionTerminal(state store.CapabilityExecutionState) bool {
	switch state {
	case store.CapabilityExecutionSucceeded, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown,
		store.CapabilityExecutionDenied, store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
		return true
	default:
		return false
	}
}

func capabilityExecutionDiagnostic(status commands.CapabilityOutcomeStatus) error {
	return fmt.Errorf("capability returned %s outcome", status)
}

func claimCapabilityExecutionForJob(ctx context.Context, jobs JobRepository, job store.ConversationJob, id string) (store.CapabilityExecution, bool, error) {
	return jobs.ClaimCapabilityExecutionForJob(ctx, id, job.ID, job.LeaseToken)
}

func finishCapabilityExecutionUnknown(ctx context.Context, jobs JobRepository, execution store.CapabilityExecution, result, reason string) (store.CapabilityExecution, error) {
	return jobs.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, result, reason)
}

const staleCapabilityUnknownReason = "进程中断，外部操作结果未知；系统没有自动重试"

func markStaleCapabilityExecutionUnknown(ctx context.Context, jobs JobRepository, job store.ConversationJob, execution store.CapabilityExecution) (store.CapabilityExecution, bool, error) {
	return jobs.MarkStaleCapabilityExecutionUnknown(ctx, execution.ID, job.ID, job.LeaseToken, staleCapabilityUnknownReason)
}

func (c *Coordinator) executeCommandRoute(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	invocation commands.Invocation,
	commit responseCommitter,
) {
	if err := c.appendCommandEvent(ctx, job, store.ConversationEventUser, "user", strings.TrimSpace(inbound.Text)); err != nil {
		c.fail(ctx, job, err)
		return
	}
	executions, err := c.jobs.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	if len(executions) == 0 {
		validated, valid := commands.NewInvocation(invocation.ID(), invocation.Args)
		if !valid {
			input := commands.Input{Text: invocation.CanonicalCommand(), Identity: job.Identity, SuppressLog: true}
			outcome, executeErr := c.commands.ExecuteCapability(ctx, input, invocation.ID(), invocation.Args)
			if executeErr != nil {
				c.fail(ctx, job, executeErr)
				return
			}
			c.finishCommandWithoutExecution(ctx, job, inbound, outcome.Response, outcome.Status, commit)
			return
		}
		invocation = validated
	}
	if len(executions) == 0 && invocation.Policy().Confirmation == commands.ConfirmUser {
		c.prepareCommandConfirmations(ctx, job, inbound, invocation, commit)
		return
	}
	if len(executions) == 0 {
		c.executeNewCommand(ctx, job, inbound, invocation, commit)
		return
	}
	c.resumeCommand(ctx, job, inbound, executions, commit)
}

func (c *Coordinator) prepareCommandConfirmations(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	invocation commands.Invocation,
	commit responseCommitter,
) {
	input := commands.Input{Text: invocation.CanonicalCommand(), Identity: job.Identity, SuppressLog: true}
	descriptions, err := c.commands.DescribeCapabilityInvocations(ctx, input, invocation.ID(), invocation.Args)
	if err != nil {
		expanded := commands.ExpandMutationInvocations(invocation)
		failed := invocation
		if len(descriptions) < len(expanded) {
			failed = expanded[len(descriptions)]
		}
		outcome, executeErr := c.commands.ExecuteCapability(ctx, input, failed.ID(), failed.Args)
		if executeErr != nil {
			c.fail(ctx, job, executeErr)
			return
		}
		c.finishCommandWithoutExecution(ctx, job, inbound, outcome.Response, outcome.Status, commit)
		return
	}
	prepares := make([]store.CapabilityExecutionPrepare, 0, len(descriptions))
	for index, description := range descriptions {
		if !description.ConfirmationRequired {
			c.fail(ctx, job, fmt.Errorf("mutation capability %q bypassed confirmation", description.Invocation.ID()))
			return
		}
		receipt := commands.ReceiptForInvocation(description.Invocation)
		if description.Receipt != nil {
			receipt = *description.Receipt
		}
		if strings.TrimSpace(receipt.Action) == "" {
			receipt = store.CapabilityReceipt{Action: "执行", Resource: "操作", Subject: description.Invocation.CanonicalCommand()}
		}
		prepares = append(prepares, store.CapabilityExecutionPrepare{
			Identity: job.Identity, JobID: job.ID, LeaseToken: job.LeaseToken, Sequence: index,
			DedupeKey:  fmt.Sprintf("conversation-job:%d:command:operation:%d", job.ID, index),
			Capability: string(description.Invocation.ID()), Arguments: append([]string(nil), description.Invocation.Args...),
			Effect: string(description.Policy.Effect), Receipt: receipt, RequiresConfirmation: true,
		})
	}
	if _, _, err := c.jobs.PrepareCapabilityExecutions(ctx, prepares); err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	c.waitForNextCommandConfirmation(ctx, job, inbound, commands.Response{}, commit)
}

func (c *Coordinator) executeNewCommand(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	invocation commands.Invocation,
	commit responseCommitter,
) {
	receipt := commands.ReceiptForInvocation(invocation)
	execution, created, err := c.jobs.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: job.Identity, JobID: job.ID, LeaseToken: job.LeaseToken,
		DedupeKey:  fmt.Sprintf("conversation-job:%d:command:operation:0", job.ID),
		Capability: string(invocation.ID()), Arguments: append([]string(nil), invocation.Args...),
		Effect: string(invocation.Policy().Effect), Receipt: receipt,
	})
	if err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	if created {
		if execution.State == store.CapabilityExecutionRunning && capabilityExecutionIsRead(execution) {
			c.executeClaimedCommand(ctx, job, inbound, execution, invocation, false, commit)
			return
		}
		claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
		if err != nil {
			c.fail(ctx, job, markConversationPersistenceError(err))
			return
		}
		if !execute {
			c.handleCommandClaimLoser(ctx, job, inbound, claimed, commands.Response{}, commit)
			return
		}
		execution = claimed
	} else if execution.State == store.CapabilityExecutionRunning {
		if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
			claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
			if err != nil {
				c.fail(ctx, job, markConversationPersistenceError(err))
				return
			}
			if !execute {
				c.handleCommandClaimLoser(ctx, job, inbound, claimed, commands.Response{}, commit)
				return
			}
			invocation, ok := commands.RestoreInvocation(commands.CapabilityID(execution.Capability), execution.Arguments)
			if !ok {
				c.fail(ctx, job, fmt.Errorf("restore running command capability %q", execution.Capability))
				return
			}
			c.executeClaimedCommand(ctx, job, inbound, claimed, invocation, false, commit)
			return
		}
		if capabilityExecutionHasStaleLease(execution, job) {
			current, marked, err := markStaleCapabilityExecutionUnknown(ctx, c.jobs, job, execution)
			if err != nil {
				c.fail(ctx, job, markConversationPersistenceError(err))
				return
			}
			if !marked {
				c.handleCommandClaimLoser(ctx, job, inbound, current, commands.Response{}, commit)
				return
			}
			c.finishCommandBatch(ctx, job, inbound, commands.Response{}, commit)
			return
		}
		c.retryCommandBatch(ctx, job, inbound, commands.Response{}, "capability execution is still running", commit)
		return
	} else if execution.State == store.CapabilityExecutionAwaitingConfirmation {
		c.waitForNextCommandConfirmation(ctx, job, inbound, commands.Response{}, commit)
		return
	} else if execution.State == store.CapabilityExecutionApproved || execution.State == store.CapabilityExecutionWaitingAuth {
		claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
		if err != nil {
			c.fail(ctx, job, markConversationPersistenceError(err))
			return
		}
		if !execute {
			c.handleCommandClaimLoser(ctx, job, inbound, claimed, commands.Response{}, commit)
			return
		}
		execution = claimed
	}
	if execution.State != store.CapabilityExecutionRunning {
		c.finishCommandBatch(ctx, job, inbound, commands.Response{}, commit)
		return
	}
	c.executeClaimedCommand(ctx, job, inbound, execution, invocation, false, commit)
}

func (c *Coordinator) resumeCommand(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	executions []store.CapabilityExecution,
	commit responseCommitter,
) {
	response := commands.Response{}
	for _, execution := range executions {
		switch execution.State {
		case store.CapabilityExecutionAwaitingConfirmation:
			c.waitForNextCommandConfirmation(ctx, job, inbound, response, commit)
			return
		case store.CapabilityExecutionApproved, store.CapabilityExecutionWaitingAuth:
			claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
			if err != nil {
				c.fail(ctx, job, markConversationPersistenceError(err))
				return
			}
			if !execute {
				c.handleCommandClaimLoser(ctx, job, inbound, claimed, response, commit)
				return
			}
			invocation, ok := commands.RestoreInvocation(commands.CapabilityID(claimed.Capability), claimed.Arguments)
			if !ok {
				c.fail(ctx, job, fmt.Errorf("restore command capability %q", claimed.Capability))
				return
			}
			c.executeClaimedCommand(ctx, job, inbound, claimed, invocation, true, commit)
			return
		case store.CapabilityExecutionRunning:
			invocation, ok := commands.RestoreInvocation(commands.CapabilityID(execution.Capability), execution.Arguments)
			if !ok {
				c.fail(ctx, job, fmt.Errorf("restore running command capability %q", execution.Capability))
				return
			}
			if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
				claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
				if err != nil {
					c.fail(ctx, job, markConversationPersistenceError(err))
					return
				}
				if !execute {
					c.handleCommandClaimLoser(ctx, job, inbound, claimed, response, commit)
					return
				}
				c.executeClaimedCommand(ctx, job, inbound, claimed, invocation, invocation.Policy().Confirmation == commands.ConfirmUser, commit)
				return
			}
			if capabilityExecutionHasStaleLease(execution, job) {
				current, marked, err := markStaleCapabilityExecutionUnknown(ctx, c.jobs, job, execution)
				if err != nil {
					c.fail(ctx, job, markConversationPersistenceError(err))
					return
				}
				if !marked {
					c.handleCommandClaimLoser(ctx, job, inbound, current, response, commit)
					return
				}
				c.finishCommandBatch(ctx, job, inbound, response, commit)
				return
			}
			c.retryCommandBatch(ctx, job, inbound, response, "capability execution is still running", commit)
			return
		case store.CapabilityExecutionSucceeded, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
			if execution.ReceiptState != execution.State && strings.TrimSpace(capabilityExecutionModelResult(execution)) != "" {
				response.Text = joinResponseText(response.Text, capabilityExecutionModelResult(execution))
				response.Kind = execution.Capability
			}
		case store.CapabilityExecutionDenied:
			if err := c.appendCommandEvent(ctx, job, store.ConversationEventAssistant, execution.ID, "用户拒绝执行该操作。"); err != nil {
				c.fail(ctx, job, err)
				return
			}
		}
	}
	c.finishCommandBatch(ctx, job, inbound, response, commit)
}

func (c *Coordinator) handleCommandClaimLoser(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	execution store.CapabilityExecution,
	response commands.Response,
	commit responseCommitter,
) {
	switch execution.State {
	case store.CapabilityExecutionAwaitingConfirmation:
		c.waitForNextCommandConfirmation(ctx, job, inbound, response, commit)
	case store.CapabilityExecutionRunning:
		if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
			current, marked, err := markStaleCapabilityExecutionUnknown(ctx, c.jobs, job, execution)
			if err != nil {
				c.fail(ctx, job, markConversationPersistenceError(err))
				return
			}
			if !marked {
				if capabilityExecutionTerminal(current.State) {
					c.finishCommandBatch(ctx, job, inbound, response, commit)
					return
				}
				c.retryCommandBatch(ctx, job, inbound, response, "capability execution ownership changed", commit)
				return
			}
			c.finishCommandBatch(ctx, job, inbound, response, commit)
			return
		}
		c.retryCommandBatch(ctx, job, inbound, response, "capability execution is still running", commit)
	case store.CapabilityExecutionApproved, store.CapabilityExecutionWaitingAuth:
		c.retryCommandBatch(ctx, job, inbound, response, "capability execution is still owned by another worker", commit)
	default:
		c.finishCommandBatch(ctx, job, inbound, response, commit)
	}
}

func (c *Coordinator) executeClaimedCommand(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	execution store.CapabilityExecution,
	invocation commands.Invocation,
	approved bool,
	commit responseCommitter,
) {
	input := commands.Input{Text: invocation.CanonicalCommand(), Identity: job.Identity, SuppressLog: true}
	var (
		outcome commands.CapabilityOutcome
		err     error
	)
	if approved {
		receipt := execution.Receipt
		outcome, err = c.commands.ExecuteApprovedInvocation(ctx, input, commands.CapabilityInvocationDescription{
			Invocation: invocation, Policy: invocation.Policy(), ConfirmationRequired: true, Receipt: &receipt,
		})
	} else {
		outcome, err = c.commands.ExecuteCapability(ctx, input, invocation.ID(), invocation.Args)
	}
	if err != nil {
		if _, finishErr := c.jobs.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "", err); finishErr != nil {
			c.fail(ctx, job, markConversationPersistenceError(finishErr))
			return
		}
		c.fail(ctx, job, err)
		return
	}
	if outcome.Receipt != nil && *outcome.Receipt != execution.Receipt {
		if err := c.jobs.UpdateCapabilityExecutionReceipt(ctx, execution.ID, execution.LeaseToken, *outcome.Receipt); err != nil {
			c.fail(ctx, job, markConversationPersistenceError(err))
			return
		}
	}
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		if _, err := c.jobs.DeferCapabilityExecutionForAuth(ctx, execution.ID, execution.LeaseToken); err != nil {
			c.fail(ctx, job, markConversationPersistenceError(err))
			return
		}
		if err := commit(ctx, outcome.Response, nil, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
		}); err != nil {
			c.fail(ctx, job, err)
			return
		}
		c.recordJob(ctx, job, inbound, outcome.Response, store.InteractionStatusWaitingAuth)
		return
	}
	var outcomeErr error
	if capabilityOutcomeIsUnknown(outcome) {
		finished, finishErr := finishCapabilityExecutionUnknown(ctx, c.jobs, execution,
			strings.TrimSpace(outcome.Response.Text), capabilityExecutionDiagnostic(outcome.Status).Error())
		if finishErr != nil {
			c.fail(ctx, job, markConversationPersistenceError(finishErr))
			return
		}
		if text := strings.TrimSpace(finished.Result); text != "" {
			if err := c.appendCommandEvent(ctx, job, store.ConversationEventAssistant, finished.ID, text); err != nil {
				c.fail(ctx, job, err)
				return
			}
		}
		c.finishCommandBatch(ctx, job, inbound, outcome.Response, commit)
		return
	}
	if outcome.Status != commands.CapabilityOutcomeSuccess {
		outcomeErr = capabilityExecutionDiagnostic(outcome.Status)
	}
	finished, err := c.jobs.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, strings.TrimSpace(outcome.Response.Text), outcomeErr)
	if err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	if text := strings.TrimSpace(outcome.Response.Text); text != "" {
		if err := c.appendCommandEvent(ctx, job, store.ConversationEventAssistant, finished.ID, text); err != nil {
			c.fail(ctx, job, err)
			return
		}
	}
	c.finishCommandBatch(ctx, job, inbound, outcome.Response, commit)
}

func (c *Coordinator) finishCommandBatch(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	response commands.Response,
	commit responseCommitter,
) {
	executions, err := c.jobs.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	for _, execution := range executions {
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			c.waitForNextCommandConfirmation(ctx, job, inbound, response, commit)
			return
		}
		if execution.State == store.CapabilityExecutionRunning {
			if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
				current, marked, err := markStaleCapabilityExecutionUnknown(ctx, c.jobs, job, execution)
				if err != nil {
					c.fail(ctx, job, markConversationPersistenceError(err))
					return
				}
				if !marked && !capabilityExecutionTerminal(current.State) {
					c.retryCommandBatch(ctx, job, inbound, response, "capability execution ownership changed", commit)
					return
				}
				continue
			}
			c.retryCommandBatch(ctx, job, inbound, response, "capability execution is still running", commit)
			return
		}
		if execution.State == store.CapabilityExecutionApproved || execution.State == store.CapabilityExecutionWaitingAuth {
			c.retryCommandBatch(ctx, job, inbound, response, "capability execution is not terminal", commit)
			return
		}
	}
	receipts, err := c.unsentExecutionReceipts(ctx, job.ID, false)
	if err != nil {
		c.fail(ctx, job, err)
		return
	}
	visible := appendReceiptLines(response, "", receipts)
	if err := commit(ctx, visible, receipts.IDs, store.ConversationJobTransition{State: store.ConversationJobStateCompleted}); err != nil {
		c.fail(ctx, job, err)
		return
	}
	c.recordJob(ctx, job, inbound, response, store.InteractionStatusHandled)
}

func (c *Coordinator) retryCommandBatch(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	response commands.Response,
	reason string,
	commit responseCommitter,
) {
	if err := commit(ctx, response, nil, store.ConversationJobTransition{
		State: store.ConversationJobStateRetryWait, LastError: reason,
	}); err != nil {
		c.fail(ctx, job, err)
		return
	}
	c.recordJob(ctx, job, inbound, response, store.InteractionStatusAccepted)
}

func (c *Coordinator) waitForNextCommandConfirmation(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	response commands.Response,
	commit responseCommitter,
) {
	receipts, err := c.unsentExecutionReceipts(ctx, job.ID, true)
	if err != nil {
		c.fail(ctx, job, err)
		return
	}
	visible := appendReceiptLines(response, confirmationPrompt, receipts)
	visible.Kind = "command_confirmation"
	if err := commit(ctx, visible, receipts.IDs, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil {
		c.fail(ctx, job, err)
		return
	}
	c.recordJob(ctx, job, inbound, visible, store.InteractionStatusWaitingConfirmation)
}

func (c *Coordinator) finishCommandWithoutExecution(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	response commands.Response,
	status commands.CapabilityOutcomeStatus,
	commit responseCommitter,
) {
	transition := store.ConversationJobTransition{State: store.ConversationJobStateCompleted}
	if status == commands.CapabilityOutcomeAuthRequired {
		transition = store.ConversationJobTransition{State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth}
	}
	if text := strings.TrimSpace(response.Text); text != "" {
		if err := c.appendCommandEvent(ctx, job, store.ConversationEventAssistant, "result", text); err != nil {
			c.fail(ctx, job, err)
			return
		}
	}
	if err := commit(ctx, response, nil, transition); err != nil {
		c.fail(ctx, job, err)
		return
	}
	interactionStatus := store.InteractionStatusHandled
	if status == commands.CapabilityOutcomeAuthRequired {
		interactionStatus = store.InteractionStatusWaitingAuth
	}
	c.recordJob(ctx, job, inbound, response, interactionStatus)
	if status == commands.CapabilityOutcomeAuthRequired {
		return
	}
}

func (c *Coordinator) appendCommandEvent(
	ctx context.Context,
	job store.ConversationJob,
	eventType store.ConversationEventType,
	suffix string,
	content string,
) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	_, _, err := c.jobs.AppendConversationEvent(ctx, store.ConversationEvent{
		Identity: job.Identity, JobID: job.ID, JobRevision: job.Revision, JobLeaseToken: job.LeaseToken,
		DedupeKey: fmt.Sprintf("conversation-job:%d:command:%s", job.ID, strings.TrimSpace(suffix)),
		Type:      eventType, Content: content,
	})
	return markConversationPersistenceError(err)
}

func joinResponseText(current, next string) string {
	parts := make([]string, 0, 2)
	if current = strings.TrimSpace(current); current != "" {
		parts = append(parts, current)
	}
	if next = strings.TrimSpace(next); next != "" {
		parts = append(parts, next)
	}
	return strings.Join(parts, "\n\n")
}

func capabilityExecutionModelResult(execution store.CapabilityExecution) string {
	switch execution.State {
	case store.CapabilityExecutionSucceeded:
		if result := strings.TrimSpace(execution.Result); result != "" {
			return result
		}
		return "操作已完成，但没有返回内容。"
	case store.CapabilityExecutionFailed:
		if result := strings.TrimSpace(execution.Result); result != "" {
			return result
		}
		return "操作失败，未返回可用结果。"
	case store.CapabilityExecutionUnknown:
		if result := strings.TrimSpace(execution.Result); result != "" {
			return result
		}
		return "操作结果未知，系统没有自动重试。"
	case store.CapabilityExecutionDenied, store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
		return strings.TrimSpace(execution.Error)
	default:
		return ""
	}
}
