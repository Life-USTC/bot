package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestModelChoosesToolsAndCanClarifyWithoutCapabilityExecution(t *testing.T) {
	for _, test := range []struct {
		name, text, tool, arguments string
	}{
		{name: "mutation clarification", text: "帮我把自适应控制的课程标记为旁听课程"},
		{name: "verification", text: "你确定吗"},
		{name: "campus lookup", text: "查询第二课堂活动"},
		{name: "after command search", text: "帮我把自适应控制的课程标记为旁听课程", tool: commandSearchToolName, arguments: `{"query":"标记课程为旁听课程"}`},
		{name: "direct tool without search", text: "你确定吗", tool: "get_current_time", arguments: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "choice", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			const answer = "请提供具体教学班，以便确认要修改的课程。"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := requests.Add(1)
				var body struct {
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
					Messages []struct {
						Role    string
						Content json.RawMessage
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				offered := map[string]bool{}
				for _, tool := range body.Tools {
					offered[tool.Function.Name] = true
				}
				for _, name := range []string{commandSearchToolName, capabilityToolName, "get_current_time"} {
					if !offered[name] {
						t.Errorf("request %d restricted tools: missing %s", request, name)
					}
				}
				for _, message := range body.Messages {
					if strings.Contains(string(message.Content), "CURRENT TURN REQUIREMENT:") || strings.Contains(string(message.Content), "Your entire response must be a call to") {
						t.Errorf("request %d contains forced tool instruction: %s", request, message.Content)
					}
				}
				message := map[string]any{"role": "assistant", "content": answer}
				reason := "stop"
				if test.tool != "" && request == 1 {
					message["content"] = "我先查看可用的信息。"
					message["tool_calls"] = []any{map[string]any{"id": "chosen-tool", "type": "function", "function": map[string]any{"name": test.tool, "arguments": test.arguments}}}
					reason = "tool_calls"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "choice", "object": "chat.completion", "model": "test-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
			}))
			defer server.Close()
			svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, commands.Handler{Store: db}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result := svc.Run(t.Context(), claimAgentInput(t, db, ident, Input{Text: test.text, Identity: ident, JobID: job.ID}))
			if !result.Handled || result.State != RunStateCompleted || result.Response.Text != answer {
				t.Fatalf("result=%#v", result)
			}
			events, err := db.RecentConversationEvents(t.Context(), ident, 10)
			if err != nil {
				t.Fatal(err)
			}
			if test.tool != "" {
				if len(events) != 4 || events[1].Content != "我先查看可用的信息。" || len(events[1].ToolCalls) != 1 || events[2].Type != store.ConversationEventToolResult || events[3].Content != answer {
					t.Fatalf("tool transcript was rewritten: %#v", events)
				}
			} else if len(events) != 2 || events[1].Content != answer {
				t.Fatalf("answer transcript=%#v", events)
			}
			want := int32(1)
			if test.tool != "" {
				want = 2
			}
			if requests.Load() != want {
				t.Fatalf("model requests=%d want=%d", requests.Load(), want)
			}
		})
	}
}
