package botapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

type responseCommitter func(context.Context, commands.Response, []string, store.ConversationJobTransition) error

type leaseAwareCapabilityExecutionRepository interface {
	ClaimCapabilityExecutionForJob(context.Context, string, int64, string) (store.CapabilityExecution, bool, error)
}

type capabilityExecutionBatchPreparer interface {
	PrepareCapabilityExecutions(context.Context, []store.CapabilityExecutionPrepare) ([]store.CapabilityExecution, bool, error)
}

type unknownCapabilityExecutionFinisher interface {
	FinishCapabilityExecutionUnknown(context.Context, string, string, string) (store.CapabilityExecution, error)
}

const capabilityOutcomeUnknown commands.CapabilityOutcomeStatus = "unknown"

func capabilityOutcomeIsUnknown(outcome commands.CapabilityOutcome) bool {
	return outcome.Status == capabilityOutcomeUnknown
}

func capabilityExecutionIsRead(execution store.CapabilityExecution) bool {
	return strings.EqualFold(strings.TrimSpace(execution.Effect), string(commands.EffectRead))
}

func capabilityExecutionHasStaleLease(execution store.CapabilityExecution, job store.ConversationJob) bool {
	owner := strings.TrimSpace(execution.LeaseToken)
	current := strings.TrimSpace(job.LeaseToken)
	return owner != "" && owner != current
}

func capabilityExecutionDiagnostic(status commands.CapabilityOutcomeStatus) error {
	return fmt.Errorf("capability returned %s outcome", status)
}

func claimCapabilityExecutionForJob(ctx context.Context, jobs JobRepository, job store.ConversationJob, id string) (store.CapabilityExecution, bool, error) {
	claimer, ok := jobs.(leaseAwareCapabilityExecutionRepository)
	if !ok {
		return store.CapabilityExecution{}, false, errors.New("capability execution repository does not support lease-aware claims")
	}
	return claimer.ClaimCapabilityExecutionForJob(ctx, id, job.ID, job.LeaseToken)
}

func finishCapabilityExecutionUnknown(ctx context.Context, jobs JobRepository, id, result, reason string) (store.CapabilityExecution, error) {
	finisher, ok := jobs.(unknownCapabilityExecutionFinisher)
	if !ok {
		return store.CapabilityExecution{}, errors.New("capability execution repository does not support unknown outcomes")
	}
	return finisher.FinishCapabilityExecutionUnknown(ctx, id, result, reason)
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
		c.fail(ctx, job, err)
		return
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
	preparer, ok := c.jobs.(capabilityExecutionBatchPreparer)
	if !ok {
		c.fail(ctx, job, errors.New("capability execution repository does not support atomic batches"))
		return
	}
	if _, _, err := preparer.PrepareCapabilityExecutions(ctx, prepares); err != nil {
		c.fail(ctx, job, err)
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
		c.fail(ctx, job, err)
		return
	}
	if created {
		if execution.State == store.CapabilityExecutionRunning && capabilityExecutionIsRead(execution) {
			c.executeClaimedCommand(ctx, job, inbound, execution, invocation, false, commit)
			return
		}
		claimed, execute, err := claimCapabilityExecutionForJob(ctx, c.jobs, job, execution.ID)
		if err != nil {
			c.fail(ctx, job, err)
			return
		}
		if !execute {
			c.handleCommandClaimLoser(ctx, job, inbound, claimed, commands.Response{}, commit)
			return
		}
		execution = claimed
	} else if execution.State == store.CapabilityExecutionRunning {
		if capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
			invocation, ok := commands.RestoreInvocation(commands.CapabilityID(execution.Capability), execution.Arguments)
			if !ok {
				c.fail(ctx, job, fmt.Errorf("restore running command capability %q", execution.Capability))
				return
			}
			c.executeClaimedCommand(ctx, job, inbound, execution, invocation, false, commit)
			return
		}
		if capabilityExecutionHasStaleLease(execution, job) {
			if err := c.jobs.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
				c.fail(ctx, job, err)
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
			c.fail(ctx, job, err)
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
				c.fail(ctx, job, err)
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
				c.executeClaimedCommand(ctx, job, inbound, execution, invocation, invocation.Policy().Confirmation == commands.ConfirmUser, commit)
				return
			}
			if capabilityExecutionHasStaleLease(execution, job) {
				if err := c.jobs.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
					c.fail(ctx, job, err)
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
			if err := c.jobs.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
				c.fail(ctx, job, err)
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
		_, _ = c.jobs.FinishCapabilityExecution(ctx, execution.ID, "", err)
		c.fail(ctx, job, err)
		return
	}
	if outcome.Receipt != nil && *outcome.Receipt != execution.Receipt {
		if err := c.jobs.UpdateCapabilityExecutionReceipt(ctx, execution.ID, *outcome.Receipt); err != nil {
			c.fail(ctx, job, err)
			return
		}
	}
	if outcome.Status == commands.CapabilityOutcomeAuthRequired {
		if _, err := c.jobs.DeferCapabilityExecutionForAuth(ctx, execution.ID); err != nil {
			c.fail(ctx, job, err)
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
		finished, finishErr := finishCapabilityExecutionUnknown(ctx, c.jobs, execution.ID,
			strings.TrimSpace(outcome.Response.Text), capabilityExecutionDiagnostic(outcome.Status).Error())
		if finishErr != nil {
			c.fail(ctx, job, finishErr)
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
	finished, err := c.jobs.FinishCapabilityExecution(ctx, execution.ID, strings.TrimSpace(outcome.Response.Text), outcomeErr)
	if err != nil {
		c.fail(ctx, job, err)
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
		c.fail(ctx, job, err)
		return
	}
	for _, execution := range executions {
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			c.waitForNextCommandConfirmation(ctx, job, inbound, response, commit)
			return
		}
		if execution.State == store.CapabilityExecutionRunning {
			if !capabilityExecutionIsRead(execution) && capabilityExecutionHasStaleLease(execution, job) {
				if err := c.jobs.MarkCapabilityExecutionUnknown(ctx, execution.ID, "进程中断，外部操作结果未知；系统没有自动重试"); err != nil {
					c.fail(ctx, job, err)
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
		Identity: job.Identity, JobID: job.ID,
		DedupeKey: fmt.Sprintf("conversation-job:%d:command:%s", job.ID, strings.TrimSpace(suffix)),
		Type:      eventType, Content: content,
	})
	return err
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
	case store.CapabilityExecutionSucceeded, store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
		return strings.TrimSpace(execution.Result)
	case store.CapabilityExecutionDenied, store.CapabilityExecutionCancelled, store.CapabilityExecutionExpired:
		return strings.TrimSpace(execution.Error)
	default:
		return ""
	}
}
