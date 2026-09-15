package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Existing scenarios describe complete provider messages. Serve those same
// fixtures through SSE when the production runner requests streaming, keeping
// scenario assertions about commands, confirmations and history unchanged.
// Streaming failure/backpressure tests use native SSE servers separately.
func newAgentTestServer(handler http.Handler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") || r.Body == nil {
			handler.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &request)
		if !request.Stream {
			handler.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		for k, values := range recorder.Header() {
			w.Header()[k] = values
		}
		var response map[string]json.RawMessage
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response["choices"] == nil {
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Type", "text/event-stream")
		var choices []struct {
			Message      map[string]any `json:"message"`
			FinishReason any            `json:"finish_reason"`
		}
		_ = json.Unmarshal(response["choices"], &choices)
		for index, choice := range choices {
			if calls, ok := choice.Message["tool_calls"].([]any); ok {
				for i, call := range calls {
					call.(map[string]any)["index"] = i
				}
			}
			chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": index, "delta": choice.Message, "finish_reason": choice.FinishReason}}}
			encoded, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		if usage := response["usage"]; usage != nil {
			response["choices"] = json.RawMessage(`[]`)
			encoded, _ := json.Marshal(response)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}
