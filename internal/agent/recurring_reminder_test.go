package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func TestRecurringReminderModelCreationReturnsDataAndSelectedImage(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var calls atomic.Int32
	var imageID string
	model := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"create-reminder","type":"function","function":{"name":"run_bot_command","arguments":"{\"command\":\"周期提醒 添加 每天 09:00 喝水\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, msg := range request.Messages {
			if msg.Role != "tool" {
				continue
			}
			var encoded string
			if err := json.Unmarshal(msg.Content, &encoded); err != nil {
				t.Error(err)
				continue
			}
			var result struct {
				Operation string
				Status    string
				Result    struct {
					Operation string
					Reminder  store.RecurringReminder
				}
				Images []toolresult.ImageReference
			}
			if err := json.Unmarshal([]byte(encoded), &result); err != nil {
				t.Error(err)
				continue
			}
			if result.Operation != "reminder" {
				continue
			}
			if result.Status != "succeeded" || result.Result.Operation != "recurring_reminder_add" || result.Result.Reminder.Text != "喝水" || result.Result.Reminder.NextAt == nil || len(result.Images) != 1 {
				t.Errorf("reminder tool result=%s", encoded)
				continue
			}
			imageID = result.Images[0].ID
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "![](" + imageID + ")"}, "finish_reason": "stop",
		}}})
	}))
	t.Cleanup(model.Close)
	svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: model.URL, Model: "test"}, commands.Handler{Store: db}, model.Client())
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "create-reminder", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "每天九点提醒我喝水", Identity: ident, JobID: job.ID, SendResponse: func(context.Context, store.Identity, commands.Response) error {
		t.Error("reminder image sent before model selected it")
		return nil
	}})
	result := svc.Run(t.Context(), input)
	if result.State != RunStateCompleted || calls.Load() != 2 || imageID == "" || len(result.Response.Parts) != 1 || result.Response.Parts[0].Image == nil || !strings.Contains(result.Response.Parts[0].Image.RichText, "喝水") {
		t.Fatalf("run=%#v calls=%d imageID=%q", result, calls.Load(), imageID)
	}
	rows, err := db.RecurringReminders(t.Context(), ident)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rules=%#v err=%v", rows, err)
	}
	executions, err := db.CapabilityExecutionsForJob(t.Context(), job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded || executions[0].ConfirmedAt != nil {
		t.Fatalf("ordinary creation requested confirmation: %#v err=%v", executions, err)
	}
}

func TestRecurringReminderModelDeletionWaitsForConfirmation(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			rule, err := db.SaveRecurringReminder(t.Context(), ident, 0, store.RecurringReminderSpec{Text: "喝水", Frequency: "daily", TimeOfDay: "09:00"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			model := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				message := map[string]any{"role": "assistant", "content": "已处理。"}
				finish := "stop"
				if calls.Add(1) == 1 {
					args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("周期提醒 删除 %d", rule.ID)})
					message["content"] = ""
					message["tool_calls"] = []any{map[string]any{"id": "delete-reminder", "type": "function", "function": map[string]any{"name": "run_bot_command", "arguments": string(args)}}}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
			}))
			t.Cleanup(model.Close)
			svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: model.URL, Model: "test"}, commands.Handler{Store: db}, model.Client())
			if err != nil {
				t.Fatal(err)
			}
			job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "delete-reminder", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			input := claimAgentInput(t, db, ident, Input{Text: "删除喝水提醒", Identity: ident, JobID: job.ID})
			first := svc.Run(t.Context(), input)
			if first.State != RunStateInterrupted {
				t.Fatalf("deletion did not wait: %#v", first)
			}
			if _, err := db.RecurringReminder(t.Context(), ident, rule.ID); err != nil {
				t.Fatalf("reminder changed before confirmation: %v", err)
			}
			executions, err := db.CapabilityExecutionsForJob(t.Context(), job.ID)
			if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionAwaitingConfirmation || !strings.Contains(executions[0].Receipt.Subject, "喝水") {
				t.Fatalf("pending deletion=%#v err=%v", executions, err)
			}
			commitAgentConfirmationReceipt(t, db, t.Context(), ident, job.ID, input.JobLeaseToken, "delete-reminder-confirm")
			if _, released, err := db.ResolveCapabilityConfirmation(t.Context(), ident, store.CapabilityConfirmationDecision{Approved: approved, SourceEventID: "reminder-decision"}); err != nil || released == nil {
				t.Fatalf("confirmation released=%#v err=%v", released, err)
			}
			input = claimAgentInput(t, db, ident, Input{Text: "删除喝水提醒", Identity: ident, JobID: job.ID})
			final := svc.Run(t.Context(), input)
			if final.State != RunStateCompleted || calls.Load() != 2 {
				t.Fatalf("resumed=%#v calls=%d", final, calls.Load())
			}
			_, err = db.RecurringReminder(t.Context(), ident, rule.ID)
			if approved && !errors.Is(err, store.ErrRecurringReminderNotFound) || !approved && err != nil {
				t.Fatalf("approved=%v reminder lookup=%v", approved, err)
			}
		})
	}
}
