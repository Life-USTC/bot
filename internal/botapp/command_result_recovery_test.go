package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type assistantEventFaultStore struct {
	*store.Store
	mu    sync.Mutex
	calls int
}

func (s *assistantEventFaultStore) AppendConversationEvent(ctx context.Context, event store.ConversationEvent) (store.ConversationEvent, bool, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 2 {
		return store.ConversationEvent{}, false, errors.New("injected assistant event persistence failure")
	}
	return s.Store.AppendConversationEvent(ctx, event)
}

func TestDirectCommandRecoversResponseDataAfterAssistantEventFailure(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &assistantEventFaultStore{Store: db}
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
		Text: "退出完成", Kind: "account", Data: map[string]any{"cleared": true, "source": "remote"},
	})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: jobs, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-event-recovery", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	assertRetryableDirectJob(t, db, job.ID)

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	assertCompletedDirectJob(t, db, job.ID)
	if handler.calls != 1 {
		t.Fatalf("direct command executed %d times, want once", handler.calls)
	}
	assertDirectCommandData(t, db, job.Identity, map[string]any{"cleared": true, "source": "remote"})
}

func TestDirectCommandRecoversResponseDataAfterOutboxFailure(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &outputCommitFaultStore{Store: db, failures: 1}
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
		Text: "退出完成", Kind: "account", Data: map[string]any{"cleared": true, "source": "remote"},
	})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: jobs, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-outbox-recovery", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	assertRetryableDirectJob(t, db, job.ID)

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	assertCompletedDirectJob(t, db, job.ID)
	if handler.calls != 1 {
		t.Fatalf("direct command executed %d times, want once", handler.calls)
	}
	assertDirectCommandData(t, db, job.Identity, map[string]any{"cleared": true, "source": "remote"})
}

func TestDirectCommandRecoversResponseImageAndPartsAfterOutboxFailure(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &outputCommitFaultStore{Store: db, failures: 1}
	image := &responses.Image{Kind: "result", Title: "结果", URL: "https://example.test/result.png"}
	partImage := &responses.Image{Kind: "part", Title: "分段", URL: "https://example.test/part.png"}
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
		Text: "主结果", Kind: "notify", Data: map[string]any{"updated": true}, Image: image,
		Parts: []commands.Response{{Text: "第一段", Kind: "part_text"}, {Kind: "part_image", Image: partImage}},
	})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: jobs, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-image-parts-recovery", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	assertRetryableDirectJob(t, db, job.ID)

	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 {
		t.Fatalf("saved direct execution=%#v err=%v", executions, err)
	}
	restored, _, ok := capabilityExecutionResultResponse(executions[0])
	if !ok || restored.Image == nil || restored.Image.URL != image.URL || len(restored.Parts) != 2 ||
		restored.Parts[0].Text != "第一段" || restored.Parts[1].Image == nil || restored.Parts[1].Image.URL != partImage.URL {
		t.Fatalf("saved response image/parts=%#v snapshot=%v", restored, ok)
	}

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	assertCompletedDirectJob(t, db, job.ID)
	if handler.calls != 1 {
		t.Fatalf("direct command executed %d times, want once", handler.calls)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("recovered response parts outbox=%#v err=%v", records, err)
	}
	if records[0].Message.Content.Text != "第一段" || records[1].Message.Content.Attachment == nil || records[1].Message.Content.Attachment.URL != partImage.URL {
		t.Fatalf("recovered response parts=%#v", records)
	}
}

func TestDeniedDirectCommandPersistsCommandResultWithoutConfirmationEvent(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{
			Text: "不应执行", Kind: "account", Data: map[string]any{"cleared": true},
		})},
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("direct-denied-event", "账户 退出")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	if err := coordinator.Enqueue(ctx, jobInbound("direct-denied-confirmation", "拒绝")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))

	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("denied command events=%#v err=%v", events, err)
	}
	if events[0].Type != store.ConversationEventUser || events[1].Type != store.ConversationEventAssistant {
		t.Fatalf("denied command transcript=%#v", events)
	}
	var result struct {
		Source    string `json:"source"`
		Operation string `json:"operation"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(events[1].Content), &result); err != nil {
		t.Fatalf("denied assistant event=%q err=%v", events[1].Content, err)
	}
	if result.Source != "bot" || result.Operation != "logout" || result.Status != "denied" {
		t.Fatalf("denied assistant result=%#v", result)
	}
}

func assertRetryableDirectJob(t *testing.T, db *store.Store, jobID int64) {
	t.Helper()
	saved, err := db.GetConversationJob(context.Background(), jobID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait {
		t.Fatalf("direct job after injected failure=%#v err=%v", saved, err)
	}
	executions, err := db.CapabilityExecutionsForJob(context.Background(), jobID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("direct execution after injected failure=%#v err=%v", executions, err)
	}
}

func assertCompletedDirectJob(t *testing.T, db *store.Store, jobID int64) {
	t.Helper()
	saved, err := db.GetConversationJob(context.Background(), jobID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("direct job after recovery=%#v err=%v", saved, err)
	}
}

func assertDirectCommandData(t *testing.T, db *store.Store, ident store.Identity, want map[string]any) {
	t.Helper()
	events, err := db.RecentConversationEvents(context.Background(), ident, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("direct command events=%#v err=%v", events, err)
	}
	var result struct {
		Source     string         `json:"source"`
		Operation  string         `json:"operation"`
		Status     string         `json:"status"`
		ObservedAt time.Time      `json:"observed_at"`
		Data       map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(events[1].Content), &result); err != nil {
		t.Fatalf("assistant command event=%q err=%v", events[1].Content, err)
	}
	if result.Source != "bot" || result.Operation != "notify" || result.Status != "succeeded" || result.ObservedAt.IsZero() {
		t.Fatalf("assistant command envelope=%#v", result)
	}
	if len(result.Data) != len(want) {
		t.Fatalf("assistant command data=%#v want=%#v", result.Data, want)
	}
	for key, value := range want {
		if result.Data[key] != value {
			t.Fatalf("assistant command data[%q]=%#v want=%#v", key, result.Data[key], value)
		}
	}
}
