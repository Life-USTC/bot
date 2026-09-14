package commands

// CapabilityOutcomeStatus is the host-facing execution state of a
// capability. The status is deliberately separate from Response: Response
// contains the domain result that may be delivered to a user, while Status is
// consumed by a host state machine.
type CapabilityOutcomeStatus string

const (
	CapabilityOutcomeSuccess      CapabilityOutcomeStatus = "success"
	CapabilityOutcomeFailed       CapabilityOutcomeStatus = "failed"
	CapabilityOutcomeUnknown      CapabilityOutcomeStatus = "unknown"
	CapabilityOutcomeAuthRequired CapabilityOutcomeStatus = "auth_required"
	CapabilityOutcomeInvalidInput CapabilityOutcomeStatus = "invalid_input"
	CapabilityOutcomeForbidden    CapabilityOutcomeStatus = "forbidden"
	CapabilityOutcomeNotFound     CapabilityOutcomeStatus = "not_found"
)

// CapabilityOutcome is the typed execution boundary for a normalized
// invocation. Response is the actual command/domain result; it is never
// synthesized from Status and does not contain a generic status envelope.
type CapabilityOutcome struct {
	Status   CapabilityOutcomeStatus `json:"status"`
	Response Response                `json:"response"`
}

func (s CapabilityOutcomeStatus) Valid() bool {
	switch s {
	case CapabilityOutcomeSuccess,
		CapabilityOutcomeFailed,
		CapabilityOutcomeUnknown,
		CapabilityOutcomeAuthRequired,
		CapabilityOutcomeInvalidInput,
		CapabilityOutcomeForbidden,
		CapabilityOutcomeNotFound:
		return true
	default:
		return false
	}
}

func (o CapabilityOutcome) Valid() bool { return o.Status.Valid() }

func SuccessOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeSuccess, Response: response}
}

func FailedOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeFailed, Response: response}
}

func UnknownOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeUnknown, Response: response}
}

func AuthRequiredOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeAuthRequired, Response: response}
}

func InvalidInputOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeInvalidInput, Response: response}
}

func ForbiddenOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeForbidden, Response: response}
}

func NotFoundOutcome(response Response) CapabilityOutcome {
	return CapabilityOutcome{Status: CapabilityOutcomeNotFound, Response: response}
}

// capabilityExecutionState is local to one executor invocation. Legacy text
// command methods can explicitly mark their domain result through Handler
// helpers without changing the direct string-returning APIs used by the
// command implementation.
type capabilityExecutionState struct {
	status CapabilityOutcomeStatus
	effect CapabilityEffect
	data   any
	// dataSet distinguishes a deliberately collected nil result from no
	// collected result. Most command data is a map or slice, but explicit
	// local operations may legitimately use nil as their result value.
	dataSet bool
}

func (h Handler) markOutcome(status CapabilityOutcomeStatus) {
	if h.execution == nil || !status.Valid() || status == CapabilityOutcomeSuccess {
		return
	}
	// Authentication is the more actionable state when a request encounters
	// an expired credential while traversing a command's domain calls.
	if h.execution.status == "" || status == CapabilityOutcomeAuthRequired {
		h.execution.status = status
	}
}

// markData records the domain value already obtained by the running
// capability. Command implementations call it immediately after a successful
// API or store read and before formatting their user-facing text.
func (h Handler) markData(data any) {
	if h.execution == nil {
		return
	}
	h.execution.data = data
	h.execution.dataSet = true
}

func outcomeFromResponse(h Handler, response Response) CapabilityOutcome {
	if response.Data == nil && h.execution != nil && h.execution.dataSet {
		response.Data = h.execution.data
	}
	status := CapabilityOutcomeSuccess
	if h.execution != nil && h.execution.status.Valid() {
		status = h.execution.status
	}
	return CapabilityOutcome{Status: status, Response: response}
}

func normalizeOutcome(outcome CapabilityOutcome) CapabilityOutcome {
	// A typed executor must return a valid status. Treat an omitted status as a
	// failed boundary contract; no text inspection is used to guess one.
	if !outcome.Status.Valid() {
		outcome.Status = CapabilityOutcomeFailed
	}
	return outcome
}
