// Package toolresult defines the model-facing result shared by commands and MCP.
// Presentation and platform delivery are deliberately separate from this data.
package toolresult

import (
	"encoding/json"
	"time"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ImageReference struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type Result struct {
	Source     string           `json:"source"`
	Operation  string           `json:"operation"`
	Status     string           `json:"status"`
	ObservedAt time.Time        `json:"observed_at"`
	Result     any              `json:"result"`
	Images     []ImageReference `json:"images,omitempty"`
	Error      *Error           `json:"error,omitempty"`
}

func Encode(source, operation, status string, observedAt time.Time, data any, err error, images ...ImageReference) string {
	if status == "success" {
		status = "succeeded"
	}
	r := Result{Source: source, Operation: operation, Status: status, ObservedAt: observedAt.UTC(), Result: data, Images: images}
	if err != nil {
		r.Error = &Error{Code: status, Message: err.Error()}
	}
	// Encode compactly. Indentation is presentation, and this payload is only
	// ever read by a model, but it is also persisted verbatim into conversation
	// history: on a real bus result the two-space indent grew one message from
	// 124,544 to 253,456 characters, so every later turn paid twice for
	// whitespace.
	encoded, encodeErr := json.Marshal(r)
	if encodeErr != nil {
		r.Status, r.Result = "failed", nil
		r.Error = &Error{Code: "invalid_result", Message: "The operation result could not be encoded."}
		encoded, _ = json.Marshal(r)
	}
	return string(encoded)
}

// Data preserves remote JSON as data, including scalar results. Plain MCP text
// stays text; no attempt is made to infer domain fields from presentation.
func Data(raw string) any {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return raw
}

// IsEncoded identifies the internal envelope before a generic tool adapter
// wraps discovery or utility output. Remote domain payloads are wrapped first.
func IsEncoded(raw string) bool {
	var value struct {
		Source     string     `json:"source"`
		Operation  string     `json:"operation"`
		Status     string     `json:"status"`
		ObservedAt *time.Time `json:"observed_at"`
	}
	return json.Unmarshal([]byte(raw), &value) == nil && value.Source != "" && value.Operation != "" && value.Status != "" && value.ObservedAt != nil
}
