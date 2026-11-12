package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
)

type todoReadDiagnostic struct {
	phase   string
	outcome string
	reason  string
}

// Observe only the final read, after the existing read retries and auth refresh.
// Keep the original error untouched; none of its text or private fields is logged.
func (h Handler) logTodoReadFailure(err error) {
	if h.Logger == nil {
		return
	}
	diagnostic := classifyTodoReadFailure(err)
	h.logf("todo read failed: operation=todo_list phase=%s outcome=%s reason=%s",
		diagnostic.phase, diagnostic.outcome, diagnostic.reason)
}

func classifyTodoReadFailure(err error) todoReadDiagnostic {
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return todoReadDiagnostic{"auth_gate", "auth_required", "not_logged_in"}
	}
	if errors.Is(err, auth.ErrReauthorizationRequired) {
		return todoReadDiagnostic{"auth_gate", "auth_required", "reauthorization_required"}
	}
	// A deadline/IO error may come from the request or a successful response body.
	// The existing wrappers do not retain that distinction, or refresh failures.
	if errors.Is(err, context.Canceled) {
		return todoReadDiagnostic{"read_unspecified", "failed", "canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return todoReadDiagnostic{"read_unspecified", "failed", "deadline"}
	}
	var httpError life.HTTPError
	if errors.As(err, &httpError) {
		diagnostic := todoReadDiagnostic{"http_response", "failed", "http_other"}
		switch httpError.StatusCode {
		case 401:
			diagnostic.outcome, diagnostic.reason = "auth_required", "http_401"
		case 403:
			diagnostic.reason = "http_403"
		case 408:
			diagnostic.reason = "http_408"
		case 429:
			diagnostic.reason = "http_429"
		default:
			if httpError.StatusCode >= 400 && httpError.StatusCode < 500 {
				diagnostic.reason = "http_4xx"
			} else if httpError.StatusCode >= 500 && httpError.StatusCode < 600 {
				diagnostic.reason = "http_5xx"
			}
		}
		return diagnostic
	}
	// JSON decoding and projection can expose the same typed errors. Do not
	// infer a finer stage from their messages, field names or values.
	var marshalError *json.MarshalerError
	if errors.As(err, &marshalError) {
		return todoReadDiagnostic{"json_processing", "failed", "json_marshal"}
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return todoReadDiagnostic{"json_processing", "failed", "json_syntax"}
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return todoReadDiagnostic{"json_processing", "failed", "json_type"}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return todoReadDiagnostic{"read_unspecified", "failed", "unexpected_eof"}
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return todoReadDiagnostic{"read_unspecified", "failed", "network_timeout"}
	}
	var urlError *url.Error
	if errors.As(err, &urlError) || errors.As(err, &networkError) {
		return todoReadDiagnostic{"read_unspecified", "failed", "network_error"}
	}
	return todoReadDiagnostic{"unknown", "failed", "unknown"}
}
