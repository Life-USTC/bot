package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

type completedAgentOutput struct {
	Identity      store.Identity
	Response      commands.Response
	HostResponses []commands.Response
	Handled       bool
	State         RunState
}

func completedOutputKey(jobID int64) string { return agentCheckpointID(jobID) + ":completed-output" }

// Run retains the completed presentation until the coordinator commits its
// Outbox. Rendering or output-commit retries must not rerun the model loop.
func (s *Service) Run(ctx context.Context, input Input) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || !s.Enabled() || input.JobID <= 0 {
		return s.runOnce(ctx, input)
	}
	failed := func(err error) Result {
		return Result{Handled: true, State: RunStateFailed, Err: markDurableAgentStateError("persist completed agent output", err)}
	}
	if s.handler.Store == nil {
		return failed(errors.New("durable agent output requires a store"))
	}
	bound, err := s.handler.Store.AgentCheckpoints().Bind(store.AgentCheckpointClaim{JobID: input.JobID, Revision: input.JobRevision, LeaseToken: input.JobLeaseToken})
	if err != nil {
		return failed(err)
	}
	key := completedOutputKey(input.JobID)
	payload, found, err := bound.Get(ctx, key)
	if err != nil {
		return failed(err)
	}
	if found {
		var saved completedAgentOutput
		if err := json.Unmarshal(payload, &saved); err != nil {
			return failed(err)
		}
		if saved.Identity != input.Identity || (saved.State != RunStateCompleted && saved.State != RunStateIgnored) {
			return failed(errors.New("completed agent output identity or state mismatch"))
		}
		for _, response := range saved.HostResponses {
			if input.SendResponse == nil {
				return failed(errors.New("host response sender is unavailable"))
			}
			if err := input.SendResponse(ctx, input.Identity, response); err != nil {
				return failed(err)
			}
		}
		return Result{Response: saved.Response, Handled: saved.Handled, State: saved.State}
	}
	saved := completedAgentOutput{Identity: input.Identity}
	originalSend := input.SendResponse
	var mu sync.Mutex
	if originalSend != nil {
		input.SendResponse = func(ctx context.Context, ident store.Identity, response commands.Response) error {
			if err := originalSend(ctx, ident, response); err != nil {
				return err
			}
			mu.Lock()
			saved.HostResponses = append(saved.HostResponses, response)
			mu.Unlock()
			return nil
		}
	}
	result := s.runOnce(ctx, input)
	if result.Err != nil || (result.State != RunStateCompleted && result.State != RunStateIgnored) {
		return result
	}
	saved.Response, saved.Handled, saved.State = result.Response, result.Handled, result.State
	payload, err = json.Marshal(saved)
	if err != nil {
		return failed(err)
	}
	if err := bound.Set(ctx, key, payload); err != nil {
		return failed(err)
	}
	return result
}
