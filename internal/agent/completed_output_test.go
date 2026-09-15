package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestCompletedImageAnswerSurvivesOutputRetryWithoutCallingModel(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	id, err := db.SaveCommandImage(t.Context(), ident, "bus-image", &responses.Image{Kind: "bus", Title: "校车图"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answer, _ := json.Marshal("说明\n![](" + id + ")")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, answer)
	}))
	defer server.Close()
	config := Config{Enabled: true, APIKey: "test", BaseURL: server.URL, Model: "test"}
	handler := commands.Handler{Store: db}
	svc, err := New(t.Context(), config, handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "image-output-retry", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "把图发给我", Identity: ident, JobID: job.ID})
	first := svc.Run(t.Context(), input)
	if first.Err != nil || first.State != RunStateCompleted || len(first.Response.Parts) != 2 || first.Response.Parts[1].Image == nil {
		t.Fatalf("first=%#v", first)
	}
	if ok, err := db.RetryConversationJob(t.Context(), job.ID, input.JobLeaseToken, "render failed"); err != nil || !ok {
		t.Fatalf("retry=%v err=%v", ok, err)
	}
	retryInput := claimAgentInput(t, db, ident, Input{Text: input.Text, Identity: ident, JobID: job.ID})
	restarted, err := New(t.Context(), config, handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	wrongActor := retryInput
	wrongActor.Identity.UserID = "43"
	if leaked := restarted.Run(t.Context(), wrongActor); leaked.State != RunStateFailed || len(leaked.Response.Parts) != 0 {
		t.Fatalf("cross-actor cached output=%#v", leaked)
	}
	second := restarted.Run(t.Context(), retryInput)
	if second.Err != nil || second.State != RunStateCompleted || len(second.Response.Parts) != 2 || second.Response.Parts[0].Text != "说明" || second.Response.Parts[1].Image == nil || second.Response.Parts[1].Image.Title != "校车图" {
		t.Fatalf("second=%#v", second)
	}
	if calls.Load() != 1 {
		t.Fatalf("model reran after output failure: %d", calls.Load())
	}
	if ok, err := db.CompleteConversationJob(t.Context(), job.ID, retryInput.JobLeaseToken); err != nil || !ok {
		t.Fatalf("complete=%v err=%v", ok, err)
	}
	if err := restarted.Acknowledge(t.Context(), job.ID, retryInput.JobRevision, retryInput.JobLeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.AgentCheckpoints().Get(t.Context(), completedOutputKey(job.ID)); err != nil || found {
		t.Fatalf("completed output retained after acknowledgement: %v %v", found, err)
	}
}
