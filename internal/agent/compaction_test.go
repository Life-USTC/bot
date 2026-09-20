package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type summaryModelFunc func(context.Context, []*schema.Message) (*schema.Message, error)

func (f summaryModelFunc) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return f(ctx, input)
}
func (f summaryModelFunc) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unexpected stream")
}

func seedCompactionHistory(t *testing.T, db *store.Store, ident store.Identity, turns, tokens int) []store.ConversationEvent {
	t.Helper()
	latest, err := db.RecentConversationEvents(t.Context(), ident, 1)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	if len(latest) > 0 {
		offset = latest[0].ID
	}
	var result []store.ConversationEvent
	for i := 0; i < turns; i++ {
		actor := ident
		actor.UserID = fmt.Sprintf("speaker-%d", i%2)
		for j, event := range []store.ConversationEvent{
			{Type: store.ConversationEventUser, Content: fmt.Sprintf("目标%d：订阅 MATH1006.01，JW179889，助教", i), ActorDisplayName: actor.UserID},
			{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{ID: fmt.Sprint(i), Name: "lookup", Arguments: `{}`}}},
			{Type: store.ConversationEventToolResult, ToolCallID: fmt.Sprint(i), ToolName: "lookup", Content: `{"status":"succeeded","result":"` + strings.Repeat("数", tokens) + `"}`},
			{Type: store.ConversationEventAssistant, Content: "已订阅 MATH1006.01，JW179889，助教。"},
		} {
			event.Identity = actor
			event.DedupeKey = fmt.Sprintf("%s-%d-%d-%d", ident.ConversationID, offset, i, j)
			event.OccurredAt = time.Date(2026, 9, 14, 12, i, 0, 0, time.UTC)
			saved, _, err := db.AppendConversationEvent(t.Context(), event)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, saved)
		}
	}
	return result
}

func TestCompactionPreservesLiveTailAndReusesPersistedSummary(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "test", ConversationType: "group", ConversationID: "group", UserID: "speaker-0"}
	events := seedCompactionHistory(t, db, ident, 6, 18_000)
	original := append([]*schema.Message{schema.SystemMessage("stable instruction")}, conversationEventMessages(events)...)
	live := []*schema.Message{schema.UserMessage("继续"), schema.AssistantMessage("", []schema.ToolCall{{ID: "live", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}}), schema.ToolMessage(`{"result":"live value not yet persisted"}`, "live")}
	original = append(original, live...)
	calls := 0
	mw := &conversationCompactionMiddleware{store: db, identity: ident, model: summaryModelFunc(func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		calls++
		if estimateMessagesTokens(input) > conversationSummaryInputTokens+2000 {
			t.Fatal("summary request exceeded bounded batch")
		}
		text := fmt.Sprint(input)
		if !strings.Contains(text, "speaker-0") || !strings.Contains(text, "2026-09-14") || !strings.Contains(text, "JW179889") {
			t.Fatal("summary input lost speaker/time/ID")
		}
		return schema.AssistantMessage("speaker-0 和 speaker-1 在 2026-09-14 请求订阅 MATH1006.01，JW179889；工具 succeeded，身份助教。待继续。", nil), nil
	})}
	state := &adk.ChatModelAgentState{Messages: original}
	_, after, err := mw.BeforeModelRewriteState(t.Context(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("summary calls=%d", calls)
	}
	if after.Messages[0] != original[0] || !reflect.DeepEqual(after.Messages[len(after.Messages)-len(live):], live) {
		t.Fatal("system or in-flight tool result was replaced")
	}
	if len(state.Messages) != len(original) {
		t.Fatal("mutated original state")
	}
	summary, found, err := db.ConversationCompaction(t.Context(), ident)
	if err != nil || !found || summary.CoveredEventID <= 0 {
		t.Fatalf("summary not committed: %v", err)
	}
	if after.Messages[1].Role != schema.Assistant || messageCursor(after.Messages[1], conversationSummaryIDKey) != summary.CoveredEventID {
		t.Fatal("summary became a user turn or lost cursor")
	}
	_, same, err := mw.BeforeModelRewriteState(t.Context(), after, nil)
	if err != nil || same != after || calls != 1 {
		t.Fatal("stable context was summarized again")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc := &Service{handler: commands.Handler{Store: db}}
	replay, err := svc.messagesFor(t.Context(), Input{Identity: ident, Text: "新的问题"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay[0], after.Messages[1]) {
		t.Fatal("restart changed the persisted summary prefix")
	}
	raw, err := db.RecentConversationEvents(t.Context(), ident, 100)
	if err != nil || len(raw) != len(events) {
		t.Fatal("compaction deleted raw events")
	}
	other := ident
	other.ConversationID = "other"
	if _, found, err := db.ConversationCompaction(t.Context(), other); err != nil || found {
		t.Fatal("summary leaked across conversations")
	}

	// A later compaction must carry the existing summary forward, rather than
	// rereading and resummarizing already-covered raw events. A pass now drains
	// every complete turn, so the follow-up history has to cross the trigger on
	// its own for a second compaction to happen at all.
	seedCompactionHistory(t, db, ident, 6, 18_000)
	replay, err = svc.messagesFor(t.Context(), Input{Identity: ident, Text: "继续处理"})
	if err != nil {
		t.Fatal(err)
	}
	mw.store = db
	mw.model = summaryModelFunc(func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		if len(input) < 2 || !reflect.DeepEqual(input[1], after.Messages[1]) {
			t.Fatal("next summary lost previous summary")
		}
		return schema.AssistantMessage("历史续接：MATH1006.01，JW179889；订阅助教已成功，新的请求待处理。", nil), nil
	})
	_, next, err := mw.BeforeModelRewriteState(t.Context(), &adk.ChatModelAgentState{Messages: replay}, nil)
	if err != nil {
		t.Fatal(err)
	}
	advanced, _, err := db.ConversationCompaction(t.Context(), ident)
	if err != nil || advanced.CoveredEventID <= summary.CoveredEventID || next.Messages[len(next.Messages)-1] != replay[len(replay)-1] {
		t.Fatal("next compaction failed to advance while preserving current input")
	}
}

func TestCompactionFailureDoesNotAdvanceHistory(t *testing.T) {
	for _, mode := range []string{"network", "empty", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ident := store.Identity{Platform: "test", ConversationType: "private", ConversationID: "one", UserID: "one"}
			events := seedCompactionHistory(t, db, ident, 6, 18_000)
			before := &adk.ChatModelAgentState{Messages: conversationEventMessages(events)}
			original := append([]*schema.Message(nil), before.Messages...)
			mw := &conversationCompactionMiddleware{store: db, identity: ident, model: summaryModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
				switch mode {
				case "network":
					return nil, errors.New("network failed")
				case "empty":
					return schema.AssistantMessage("", nil), nil
				default:
					return &schema.Message{Role: schema.Assistant, Content: "partial", ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}}, nil
				}
			})}
			_, _, err = mw.BeforeModelRewriteState(t.Context(), before, nil)
			if !errors.Is(err, errConversationCompaction) {
				t.Fatalf("error=%v", err)
			}
			saved, _, err := db.ConversationCompaction(t.Context(), ident)
			if err != nil || saved.CoveredEventID != 0 || saved.Summary != "" {
				t.Fatal("failed summary advanced history")
			}
			if !reflect.DeepEqual(before.Messages, original) {
				t.Fatal("failed summary changed exact state")
			}
			claimed, err := db.ClaimConversationCompaction(t.Context(), ident, 0, "next", time.Now().Add(time.Minute))
			if err != nil || !claimed {
				t.Fatal("failed summary left active lease")
			}
		})
	}
}

func TestCompactionTriggerIncludesToolSchemas(t *testing.T) {
	tokens, err := estimateToolInfosTokens([]*schema.ToolInfo{{Name: "large_tool", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"value": {Type: schema.String, Desc: strings.Repeat("大", 97_000)}})}})
	if err != nil || tokens < 97_000 {
		t.Fatalf("tool schema was not counted: %d %v", tokens, err)
	}
}

func TestCompactionAdvancesPastOversizedFirstTurn(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "test", ConversationType: "private", ConversationID: "oversized-first", UserID: "one"}
	events := seedCompactionHistory(t, db, ident, 1, 100_000)
	events = append(events, seedCompactionHistory(t, db, ident, 4, 5_000)...)
	messages := conversationEventMessages(events)
	calls := 0
	mw := &conversationCompactionMiddleware{store: db, identity: ident, model: summaryModelFunc(func(_ context.Context, _ []*schema.Message) (*schema.Message, error) {
		calls++
		return schema.AssistantMessage("第一轮超大工具结果已概括；后续请求待处理。", nil), nil
	})}
	_, after, err := mw.BeforeModelRewriteState(t.Context(), &adk.ChatModelAgentState{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("an oversized first turn blocked compaction forever")
	}
	if calls != 1 {
		t.Fatalf("compaction ran %d times for one oversized prefix", calls)
	}
	saved, found, err := db.ConversationCompaction(t.Context(), ident)
	if err != nil || !found || saved.CoveredEventID < events[3].ID {
		t.Fatalf("compaction did not advance past the oversized first turn: %#v %v", saved, err)
	}
	if after.Messages[0].Role != schema.Assistant || messageCursor(after.Messages[0], conversationSummaryIDKey) != saved.CoveredEventID {
		t.Fatal("rewritten state lost the summary cursor")
	}
	// One pass covers every complete turn and keeps the current one, so the
	// surviving tail starts at the last user boundary.
	tail := -1
	for i, m := range messages {
		if m.Role == schema.User {
			tail = i
		}
	}
	if tail < 0 || !reflect.DeepEqual(after.Messages[1:], messages[tail:]) {
		t.Fatal("a single pass must keep exactly the current turn verbatim")
	}
}

// The previous loop re-entered only until the total fell back under the very
// threshold that triggered it, so it stopped at the first value below the line.
// A pass must now land near the floor instead of just under the ceiling.
func TestCompactionLandsFarBelowTheTrigger(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "test", ConversationType: "private", ConversationID: "low-water", UserID: "one"}
	events := seedCompactionHistory(t, db, ident, 12, 8_000)
	messages := conversationEventMessages(events)
	before := estimateMessagesTokens(messages)
	if before < conversationHistoryTokenLimit {
		t.Fatalf("history did not cross the trigger: %d", before)
	}
	calls := 0
	mw := &conversationCompactionMiddleware{store: db, identity: ident, model: summaryModelFunc(func(_ context.Context, _ []*schema.Message) (*schema.Message, error) {
		calls++
		return schema.AssistantMessage("历史已概括。", nil), nil
	})}
	_, after, err := mw.BeforeModelRewriteState(t.Context(), &adk.ChatModelAgentState{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("one trigger must produce one summary request, got %d", calls)
	}
	got := estimateMessagesTokens(after.Messages)
	// Measured on this fixture: 97,776 tokens of history settle at about 8,200,
	// roughly a tenth of the trigger. The loop this replaced settled at ~24,500.
	if limit := conversationHistoryTokenLimit / 8; got >= limit {
		t.Fatalf("compaction settled at %d tokens, want below %d (from %d)", got, limit, before)
	}
}

func TestCompactionCannotDiscardOversizedCurrentTurn(t *testing.T) {
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.SystemMessage("system"), schema.UserMessage(strings.Repeat("数", 100_000))}}
	mw := &conversationCompactionMiddleware{}
	_, after, err := mw.BeforeModelRewriteState(t.Context(), state, nil)
	if err != nil || after != state || len(state.Messages) != 2 {
		t.Fatalf("oversized current turn was discarded or rejected: %v", err)
	}
}

func TestCompactionUsesProviderBudgetWithoutToolsOrInternalMetadata(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "test", ConversationType: "private", ConversationID: "wire", UserID: "one"}
	events := seedCompactionHistory(t, db, ident, 6, 18_000)
	requests := 0
	server := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		for _, forbidden := range []string{`"tools":`, conversationEventIDKey, conversationSummaryIDKey} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("summary request contains %s", forbidden)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"summary","choices":[{"index":0,"message":{"role":"assistant","content":"MATH1006.01，JW179889，订阅助教成功；保留待处理请求。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":72000,"completion_tokens":50,"total_tokens":72050}}`)
	}))
	defer server.Close()
	svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: server.URL, Model: "test"}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	metrics := newRunMetrics()
	usage := &usageAccumulator{}
	ctx := withUsageAccumulator(withRunMetrics(t.Context(), metrics), usage)
	_, _, err = newConversationCompactionMiddleware(svc, ident).BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: conversationEventMessages(events)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || usage.snapshot().PromptTokens != 72000 || metrics.snapshot().modelRequests != 1 {
		t.Fatalf("summary request bypassed usage accounting: requests=%d usage=%#v", requests, usage.snapshot())
	}
}

func TestCompactionRenewsLeaseDuringLongSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, err := store.Open(t.TempDir() + "/bot.db")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		ident := store.Identity{Platform: "test", ConversationType: "private", ConversationID: "long-summary", UserID: "user"}
		events := seedCompactionHistory(t, db, ident, 6, 18_000)
		messages := append(conversationEventMessages(events), schema.UserMessage("继续"))
		calls := 0
		mw := &conversationCompactionMiddleware{store: db, identity: ident, model: summaryModelFunc(func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
			calls++
			time.Sleep(10 * time.Minute)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return schema.AssistantMessage("用户要求订阅 MATH1006.01，JW179889，助教；操作已完成，等待后续请求。", nil), nil
		})}
		_, after, err := mw.BeforeModelRewriteState(t.Context(), &adk.ChatModelAgentState{Messages: messages}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || after == nil {
			t.Fatalf("calls=%d state=%v", calls, after)
		}
		saved, found, err := db.ConversationCompaction(t.Context(), ident)
		if err != nil || !found || saved.CoveredEventID == 0 {
			t.Fatalf("summary not committed: %#v %v", saved, err)
		}
	})
}
