package agent

import (
	"errors"
	"fmt"
	"strings"
)

// durableAgentStateError marks a repository failure inside a host or MCP tool.
// The Agent loop must not turn this into model prose or a terminal user reply:
// the coordinator owns the retry, while persisted capability state prevents
// an ambiguous mutation from being executed twice.
type durableAgentStateError struct {
	operation string
	err       error
}

func (e *durableAgentStateError) Error() string {
	if e == nil {
		return "persist durable agent state"
	}
	operation := strings.TrimSpace(e.operation)
	if operation == "" {
		return fmt.Sprintf("persist durable agent state: %v", e.err)
	}
	return fmt.Sprintf("persist durable agent state (%s): %v", operation, e.err)
}

func (e *durableAgentStateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func markDurableAgentStateError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var existing *durableAgentStateError
	if errors.As(err, &existing) {
		return err
	}
	return &durableAgentStateError{operation: strings.TrimSpace(operation), err: err}
}

func isDurableAgentStateError(err error) bool {
	var target *durableAgentStateError
	return errors.As(err, &target)
}
