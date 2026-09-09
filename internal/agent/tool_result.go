package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

// Host scaffolding around a tool call is machine-readable and stated in
// English. Only the domain payload is user-language text, because that text is
// the actual answer rather than something the host is saying about the call.
// The envelope reports what happened; it does not tell the model how to phrase
// a reply.
type capabilityToolResult struct {
	Capability string          `json:"capability"`
	Arguments  []string        `json:"arguments,omitempty"`
	Effect     string          `json:"effect,omitempty"`
	Outcome    string          `json:"outcome"`
	ObservedAt string          `json:"observed_at"`
	Result     string          `json:"result,omitempty"`
	Detail     string          `json:"detail,omitempty"`
	Subject    string          `json:"subject,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

type campusToolResult struct {
	Tool       string          `json:"tool"`
	Arguments  map[string]any  `json:"arguments,omitempty"`
	Outcome    string          `json:"outcome"`
	ObservedAt string          `json:"observed_at"`
	Result     json.RawMessage `json:"result,omitempty"`
	Detail     string          `json:"detail,omitempty"`
}

type hostToolRejection struct {
	Outcome string `json:"outcome"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail"`
}

const (
	toolOutcomeSucceeded = "succeeded"
	toolOutcomeFailed    = "failed"
	toolOutcomeUnknown   = "unknown"
	toolOutcomeDenied    = "denied"
	toolOutcomeCancelled = "cancelled"
	toolOutcomeExpired   = "expired"
	toolOutcomeRunning   = "running"
	toolOutcomePending   = "pending"
	toolOutcomeRejected  = "rejected"
)

// observedAt is recorded when the result is produced and persisted with it, so
// replayed history stays byte-identical and the provider prompt cache still
// hits. Rendering the time during replay would change the bytes on every turn.
func observedAt(now time.Time) string {
	return now.In(shanghaiLocation).Format(time.RFC3339)
}

func encodeToolResult(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"outcome":"failed","detail":"host could not encode the tool result"}`
	}
	return string(encoded)
}

// capabilityExecutionModelResult is the model's entire account of one
// operation: the confirmation prompt and the user's answer are not
// model-visible events. Every terminal state therefore reports its own outcome
// instead of leaving it to be inferred from a fragment of prose.
func capabilityExecutionModelResult(execution store.CapabilityExecution) string {
	envelope := capabilityToolResult{
		Capability: strings.TrimSpace(execution.Capability),
		Arguments:  append([]string(nil), execution.Arguments...),
		Effect:     strings.TrimSpace(execution.Effect),
		ObservedAt: capabilityObservedAt(execution),
		Subject:    strings.TrimSpace(execution.Receipt.Subject),
		Result:     strings.TrimSpace(execution.Result),
	}
	switch execution.State {
	case store.CapabilityExecutionSucceeded:
		envelope.Outcome = toolOutcomeSucceeded
	case store.CapabilityExecutionFailed:
		envelope.Outcome = toolOutcomeFailed
	case store.CapabilityExecutionUnknown:
		envelope.Outcome = toolOutcomeUnknown
		envelope.Detail = "the operation may or may not have taken effect; the host does not retry it"
	case store.CapabilityExecutionDenied:
		envelope.Outcome = toolOutcomeDenied
		envelope.Detail = "the user declined this operation at confirmation; nothing was changed"
		if reason := strings.TrimSpace(execution.Error); reason != "" && reason != "用户拒绝执行" {
			envelope.Detail += "; reason given: " + reason
		}
	case store.CapabilityExecutionCancelled:
		envelope.Outcome = toolOutcomeCancelled
		envelope.Detail = "the conversation job was cancelled before this operation ran"
	case store.CapabilityExecutionExpired:
		envelope.Outcome = toolOutcomeExpired
		envelope.Detail = "the confirmation window elapsed before this operation ran"
	case store.CapabilityExecutionRunning:
		envelope.Outcome = toolOutcomeRunning
	default:
		envelope.Outcome = toolOutcomePending
	}
	return encodeToolResult(envelope)
}

func capabilityObservedAt(execution store.CapabilityExecution) string {
	finished := execution.FinishedAt
	if finished == nil || finished.IsZero() {
		return observedAt(time.Now())
	}
	return observedAt(*finished)
}

// capabilityOutcomeToolResult wraps a result produced outside the durable
// execution table, so an untracked call reads exactly like a tracked one.
func capabilityOutcomeToolResult(
	id commands.CapabilityID,
	arguments []string,
	effect commands.CapabilityEffect,
	status commands.CapabilityOutcomeStatus,
	text string,
) string {
	envelope := capabilityToolResult{
		Capability: string(id),
		Arguments:  append([]string(nil), arguments...),
		Effect:     string(effect),
		Outcome:    toolOutcomeSucceeded,
		ObservedAt: observedAt(time.Now()),
		Result:     strings.TrimSpace(text),
	}
	if capabilityOutcomeIsToolError(status) {
		envelope.Outcome = toolOutcomeFailed
	}
	if status == commands.CapabilityOutcomeUnknown {
		envelope.Outcome = toolOutcomeUnknown
		envelope.Detail = "the operation may or may not have taken effect; the host does not retry it"
	}
	return encodeToolResult(envelope)
}

func campusCallToolResult(name string, arguments map[string]any, result string, callErr error) string {
	envelope := campusToolResult{
		Tool: name, Arguments: arguments,
		Outcome: toolOutcomeSucceeded, ObservedAt: observedAt(time.Now()),
	}
	if callErr != nil {
		envelope.Outcome = toolOutcomeFailed
		envelope.Detail = callErr.Error()
	}
	envelope.Result = campusResultPayload(result)
	return encodeToolResult(envelope)
}

// campusResultPayload keeps a JSON tool result as JSON instead of burying it in
// an escaped string.
func campusResultPayload(result string) json.RawMessage {
	result = strings.TrimSpace(result)
	if result == "" {
		return nil
	}
	if json.Valid([]byte(result)) {
		return json.RawMessage(result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return encoded
}

func existingCampusToolResult(execution store.CapabilityExecution) string {
	envelope := campusToolResult{
		Tool:       strings.TrimPrefix(strings.TrimSpace(execution.Capability), "mcp:"),
		ObservedAt: capabilityObservedAt(execution),
		Result:     campusResultPayload(execution.Result),
	}
	switch execution.State {
	case store.CapabilityExecutionSucceeded:
		envelope.Outcome = toolOutcomeSucceeded
	case store.CapabilityExecutionFailed:
		envelope.Outcome = toolOutcomeFailed
		envelope.Detail = strings.TrimSpace(execution.Error)
	case store.CapabilityExecutionUnknown:
		envelope.Outcome = toolOutcomeUnknown
		envelope.Detail = "the campus read may or may not have completed; the host does not retry it"
	default:
		envelope.Outcome = toolOutcomeRunning
	}
	return encodeToolResult(envelope)
}
