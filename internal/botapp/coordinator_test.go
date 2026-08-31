package botapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

type commandFunc func(context.Context, commands.Input) (commands.Response, bool)

func (fn commandFunc) HandleResponse(ctx context.Context, input commands.Input) (commands.Response, bool) {
	return fn(ctx, input)
}

type agentFunc func(context.Context, agent.Input) (commands.Response, bool)

func (fn agentFunc) HandleResponse(ctx context.Context, input agent.Input) (commands.Response, bool) {
	return fn(ctx, input)
}

type rendererFunc func(*responses.Image) ([]byte, int, int, error)

func (fn rendererFunc) RenderPNG(image *responses.Image) ([]byte, int, int, error) {
	return fn(image)
}

func newCoordinatorStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func jobInbound(eventID, text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: "napcat", UserID: "42"},
		Conversation: message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Source:       message.ReplyRef{EventID: eventID, MessageID: "message-" + eventID},
		Text:         text,
	}
}

func claimOnlyConversationJob(t *testing.T, db *store.Store) store.ConversationJob {
	t.Helper()
	job, err := db.ClaimNextConversationJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("expected one claimed conversation job")
	}
	return *job
}

func TestCoordinatorPersistsInputAndOutputExactlyOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "ping"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-1", "ping")
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != "ping" || job.Invocation.Command != "ping" {
		t.Fatalf("invocation = %#v", job.Invocation)
	}
	coordinator.execute(context.Background(), job)

	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("job = %#v", saved)
	}
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox records = %#v", records)
	}
	got := records[0].Message
	if got.Content.Text != "pong" || got.DedupeKey != "conversation-job:1:revision:1:part:0" || got.ReplyTo == nil || got.ReplyTo.EventID != "event-1" {
		t.Fatalf("outbound = %#v", got)
	}
}

func TestCoordinatorConfirmationResumesTheSameJobOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	mutations := 0
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(_ context.Context, input commands.Input) (commands.Response, bool) {
			if input.Text == "notify homework on" {
				mutations++
				return commands.Response{Text: "已开启", Kind: "notify"}, true
			}
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.WaitForConfirmation(ctx, input.Identity, "通知 作业 开"); err != nil {
				t.Fatal(err)
			}
			return commands.Response{Text: "需要确认，回复 ok。", Kind: "agent"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("event-confirm", "帮我开启作业通知")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation || saved.Invocation.Command != "notify homework on" {
		t.Fatalf("waiting job = %#v", saved)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("event-ok", "ok")); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("event-ok-duplicate", "ok")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	if mutations != 1 {
		t.Fatalf("mutation executions = %d", mutations)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Message.Content.Text != "需要确认，回复 ok。" || records[1].Message.Content.Text != "已开启" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorPersistsTextFallbackBeforeCompletingJob(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{
				Text: "fallback", Kind: "bus",
				Image: responses.NewTextImage("bus", "校车", "fallback"),
			}, true
		}),
		Outputs: db,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return nil, 0, 0, errors.New("render failed")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-2", "校车")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content.Text != "fallback" || records[0].Message.Content.Attachment != nil {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorLeaseRetryReusesOutputRevision(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "ping"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-retry-dedupe", "ping")
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if _, err := coordinator.enqueueResponse(context.Background(), job, inbound, commands.Response{Text: "pong", Kind: "ping"}, 0); err != nil {
		t.Fatal(err)
	}
	recoveryAt := time.Now().UTC().Add(3 * time.Minute)
	if err := db.RecoverConversationJobLeases(context.Background(), recoveryAt, time.Minute); err != nil {
		t.Fatal(err)
	}
	retried, err := db.ClaimConversationJob(context.Background(), job.Identity, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if retried == nil || retried.Revision != job.Revision || retried.Attempts != 2 {
		t.Fatalf("retried job = %#v", retried)
	}
	coordinator.execute(context.Background(), *retried)
	records, err := db.ClaimDue(context.Background(), recoveryAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorHostOnlyResponseIsQueuedOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.SendResponse(ctx, input.Identity, commands.Response{Text: "private-link", Kind: "subscription"}); err != nil {
				t.Fatal(err)
			}
			return commands.Response{}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-3", "给我订阅链接")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content.Text != "private-link" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorNaturalCalendarLinkRequestDeliversUsablePrivateURL(t *testing.T) {
	ctx := context.Background()
	db := newCoordinatorStore(t)
	const calendarURL = "https://calendar.example/private-token.ics"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"` + calendarURL + `"}}`))
	}))
	defer server.Close()

	identity := identityForInbound(jobInbound("identity", "ignored"))
	if err := db.SaveCredential(ctx, identity, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	handler := commands.Handler{
		Life:  life.NewClient(server.URL, server.Client()),
		Auth:  &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store: db,
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-natural-calendar-link", "能再给我发一下日历的链接吗")
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != string(commands.CapabilitySubscription) || job.Invocation.Command != "subscription link" {
		t.Fatalf("invocation = %#v", job.Invocation)
	}
	coordinator.execute(ctx, job)

	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox records = %#v", records)
	}
	reply := records[0].Message.Content.Text
	for _, want := range []string{calendarURL, "使用方法：复制链接", "通过 URL 添加/订阅日历", "自动更新", "不是 CalDAV 账户地址"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestCoordinatorHostCapabilityLoginWaitsOnAgentJob(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.SendResponse(ctx, input.Identity, commands.Response{Text: "请登录", Kind: commands.ResponseKindAuthWait}); err != nil {
				t.Fatal(err)
			}
			return commands.Response{}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-agent-auth", "给我订阅链接")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(context.Background(), job)
	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingAuth {
		t.Fatalf("agent auth job = %#v", saved)
	}
}

func TestCoordinatorLoginWaitsOnSameJob(t *testing.T) {
	db := newCoordinatorStore(t)
	authorized := false
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			if authorized {
				return commands.Response{Text: "查询完成", Kind: "schedule"}, true
			}
			return commands.Response{Text: "请登录", Kind: commands.ResponseKindAuthWait}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-4", "查询课表")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(context.Background(), job)
	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingAuth || saved.WaitReason != store.ConversationJobWaitReasonAuth {
		t.Fatalf("job = %#v", saved)
	}
	authorized = true
	if err := db.UnblockConversationJobsAfterAuth(context.Background(), saved.Identity); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted || saved.Revision != 2 || saved.Attempts != 2 {
		t.Fatalf("resumed job = %#v", saved)
	}
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Message.Content.Text != "请登录" || records[1].Message.Content.Text != "查询完成" {
		t.Fatalf("resumed outbox = %#v", records)
	}
	if records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" || records[1].Message.DedupeKey != "conversation-job:1:revision:2:part:0" {
		t.Fatalf("resumed dedupe keys = %q, %q", records[0].Message.DedupeKey, records[1].Message.DedupeKey)
	}
}
