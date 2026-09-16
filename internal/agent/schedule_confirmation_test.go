package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

// A logged-in personal read must execute without creating a confirmation.
// Exercise the actual model tool path, not only the capability's policy flag.
func TestPersonalCourseQueriesExecuteWithoutConfirmation(t *testing.T) {
	for _, command := range []string{"今天课表", "课表 2026秋", "订阅"} {
		t.Run(command, func(t *testing.T) {
			ctx := t.Context()
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var reads atomic.Int32
			api := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasPrefix(r.URL.Path, "/api/workspace/") {
					reads.Add(1)
					if r.Header.Get("Authorization") != "Bearer access" {
						t.Errorf("missing personal authorization")
					}
				}
				switch r.URL.Path {
				case "/api/workspace/schedules":
					_, _ = fmt.Fprint(w, "{\"schedules\":[]}")
				case "/api/workspace/subscriptions/current":
					_, _ = fmt.Fprint(w, "{\"subscription\":{\"sections\":[]}}")
				case "/api/catalog/semesters":
					_, _ = fmt.Fprint(w, "{\"data\":[{\"id\":1,\"nameCn\":\"2026年秋季学期\",\"startDate\":\"2026-09-01T00:00:00+08:00\",\"endDate\":\"2027-01-31T00:00:00+08:00\"}]}")
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(api.Close)
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			if err := db.SaveCredential(ctx, ident, store.Credential{ClientID: "client", AccessToken: "access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour), Resource: api.URL}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			model := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				message := map[string]any{"role": "assistant", "content": "查询完成。"}
				finish := "stop"
				if calls.Add(1) == 1 {
					args, _ := json.Marshal(map[string]string{"command": command})
					message["content"] = ""
					message["tool_calls"] = []any{map[string]any{"id": "course-read", "type": "function", "function": map[string]any{"name": "run_bot_command", "arguments": string(args)}}}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "course-query", "object": "chat.completion", "model": "test-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
			}))
			t.Cleanup(model.Close)
			manager := &auth.Manager{Server: api.URL, HTTPClient: api.Client(), Store: db}
			svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: model.URL, Model: "test-model"}, commands.Handler{Life: life.NewClient(api.URL, api.Client()), Auth: manager, Store: db}, model.Client())
			if err != nil {
				t.Fatal(err)
			}
			job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{Identity: ident, SourceEventID: "personal-course-query", Input: store.ConversationJobInput{Text: "帮我看看选课"}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			result := svc.Run(ctx, claimAgentInput(t, db, ident, Input{Text: "帮我看看选课", Identity: ident, JobID: job.ID}))
			if result.State != RunStateCompleted || reads.Load() == 0 || calls.Load() != 2 {
				t.Fatalf("result=%#v domain_reads=%d model_calls=%d", result, reads.Load(), calls.Load())
			}
			executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(executions) != 1 {
				t.Fatalf("executions=%#v err=%v", executions, err)
			}
			if execution := executions[0]; execution.Effect != string(commands.EffectRead) || execution.ConfirmedAt != nil || (execution.State != store.CapabilityExecutionSucceeded && (execution.State != store.CapabilityExecutionFailed || execution.Error != "capability returned not_found outcome")) {
				t.Fatalf("read requested confirmation or failed: %#v", execution)
			}
		})
	}
}
