package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestAgentRejectsIncompleteStreamWithoutPersistingPartialAnswer(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Stream        bool `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !request.Stream || !request.StreamOptions.IncludeUsage {
			t.Errorf("streaming usage not requested: %+v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"partial\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"这是未完成的答案\"}}]}\n\n")
	}))
	defer server.Close()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: server.URL, Model: "test-model"}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "stream-test", ConversationType: "private", ConversationID: "stream-test"}
	response, handled := svc.HandleResponse(t.Context(), Input{Text: "回答问题", Identity: ident})
	if !handled || !strings.Contains(response.Text, "出错") || strings.Contains(response.Text, "这是未完成的答案") {
		t.Fatalf("response=%#v handled=%v", response, handled)
	}
	if requests.Load() != 1 {
		t.Fatalf("stream replayed: %d calls", requests.Load())
	}
	events, err := db.RecentConversationEvents(t.Context(), ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == store.ConversationEventAssistant && strings.Contains(event.Content, "这是未完成的答案") {
			t.Fatalf("partial answer persisted: %#v", event)
		}
	}
}

func TestAgentAssemblesStreamedToolArgumentsBeforeExecution(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = io.WriteString(w, `data: {"id":"tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"search-1","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":"}}]}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, `data: {"id":"tools","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"校车\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body), `"tool_call_id":"search-1"`) || !strings.Contains(string(body), "校车") {
			t.Errorf("missing assembled tool result: %s", body)
		}
		_, _ = io.WriteString(w, `data: {"id":"final","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"可以查询校车。"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: server.URL, Model: "test-model"}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "stream-tool-test", ConversationType: "private", ConversationID: "stream-tool-test"}
	response, handled := svc.HandleResponse(t.Context(), Input{Text: "校车怎么查询", Identity: ident})
	if !handled || response.Text != "可以查询校车。" {
		t.Fatalf("response=%#v handled=%v", response, handled)
	}
	if requests.Load() != 2 {
		t.Fatalf("model calls=%d", requests.Load())
	}
}
