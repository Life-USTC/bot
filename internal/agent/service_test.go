package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestDisabledAgentDoesNotHandle(t *testing.T) {
	svc, err := New(context.Background(), Config{}, commands.Handler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := svc.Handle(context.Background(), Input{
		Text:     "帮我看看今天有什么课",
		Identity: store.Identity{ConversationType: "private"},
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestEmptyTerminalCapabilityResultsRemainExplicitEvidence(t *testing.T) {
	// A terminal state that produced no domain result must still report itself:
	// the confirmation prompt and the user's answer are not model-visible.
	execution := store.CapabilityExecution{Capability: "notify", Arguments: []string{"homework", "on"}, Effect: "write"}
	for _, test := range []struct {
		state   store.CapabilityExecutionState
		outcome string
		detail  bool
	}{
		{state: store.CapabilityExecutionSucceeded, outcome: "succeeded"},
		{state: store.CapabilityExecutionFailed, outcome: "failed"},
		{state: store.CapabilityExecutionUnknown, outcome: "unknown", detail: true},
		{state: store.CapabilityExecutionDenied, outcome: "denied", detail: true},
		{state: store.CapabilityExecutionCancelled, outcome: "cancelled", detail: true},
		{state: store.CapabilityExecutionExpired, outcome: "expired", detail: true},
	} {
		execution.State = test.state
		var envelope capabilityToolResult
		if err := json.Unmarshal([]byte(capabilityExecutionModelResult(execution)), &envelope); err != nil {
			t.Fatalf("state %s: %v", test.state, err)
		}
		if envelope.Outcome != test.outcome {
			t.Fatalf("state %s outcome=%q want %q", test.state, envelope.Outcome, test.outcome)
		}
		if test.detail && envelope.Detail == "" {
			t.Fatalf("state %s carries no detail", test.state)
		}
		if envelope.ObservedAt == "" || envelope.Capability != "notify" {
			t.Fatalf("state %s envelope=%#v", test.state, envelope)
		}
	}
	var campus campusToolResult
	if err := json.Unmarshal([]byte(existingCampusToolResult(store.CapabilityExecution{
		Capability: "mcp:search_courses", State: store.CapabilityExecutionUnknown,
	})), &campus); err != nil {
		t.Fatal(err)
	}
	if campus.Tool != "search_courses" || campus.Outcome != "unknown" || campus.Detail == "" {
		t.Fatalf("campus unknown envelope=%#v", campus)
	}
}

func TestDurableAgentRunRejectsMissingPersistenceIdentityBeforeModelCall(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc := &Service{enabled: true, handler: commands.Handler{Store: db}}
	result := svc.Run(t.Context(), Input{Text: "hello", JobID: 1})
	if result.State != RunStateFailed || result.Err == nil || !result.Handled {
		t.Fatalf("durable run without persistence identity=%#v", result)
	}
}

func TestDurableAgentRunRetriesWhenPriorAttemptBudgetCannotBeRead(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "budget-read", ConversationType: "private", ConversationID: "budget-read"}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "budget-read", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{enabled: true, handler: commands.Handler{Store: db}}
	result := svc.Run(t.Context(), Input{Text: "hello", Identity: ident, JobID: job.ID})
	if result.State != RunStateFailed || result.Err == nil || !result.Handled {
		t.Fatalf("durable run with unreadable attempt budget=%#v", result)
	}
}

func TestAgentCapabilityPersistenceFailureMarksRunRetryable(t *testing.T) {
	ctx := t.Context()
	path := t.TempDir() + "/bot.db"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	breaker, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = breaker.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "agent-persistence", ConversationType: "private", ConversationID: "agent-persistence"}
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "agent-persistence", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := breaker.ExecContext(ctx, "DROP TABLE capability_executions"); err != nil {
			t.Errorf("break capability persistence: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-persistence","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
				"id":"call-persistence","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"ping\",\"arguments\":[]}"
			}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Logger: log.New(&logs, "", 0)},
		commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result := svc.Run(ctx, Input{
		Text: "你好呀", Identity: ident, JobID: job.ID,
		JobRevision: claimed.Revision, JobLeaseToken: claimed.LeaseToken,
	})
	if !result.Handled || result.State != RunStateFailed || result.Err == nil {
		t.Fatalf("agent persistence result=%#v logs=%q", result, logs.String())
	}
	if !isDurableAgentStateError(result.Err) {
		t.Fatalf("agent persistence error was not classified: %T %v", result.Err, result.Err)
	}
	if strings.Contains(result.Response.Text, "capability_executions") || strings.Contains(result.Response.Text, "database") {
		t.Fatalf("persistence diagnostic exposed to user: %q", result.Response.Text)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests=%d want=1", requests.Load())
	}
}

func TestGroupAgentOnlyAdvertisesPublicCapabilities(t *testing.T) {
	description := hostCapabilityToolDescription(true)
	if !strings.Contains(description, "shared conversation") {
		t.Fatalf("group description lacks shared boundary: %q", description)
	}
	if strings.Contains(description, "course:") || strings.Contains(description, "subscription:") {
		t.Fatalf("invoke description should not eagerly dump the registry: %q", description)
	}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "100"}
	public, err := searchCommandDocumentation(ident, commandSearchInput{Query: "course"})
	if err != nil || !strings.Contains(public, `"id":"course"`) {
		t.Fatalf("public search=%q err=%v", public, err)
	}
	private, err := searchCommandDocumentation(ident, commandSearchInput{Query: "subscription"})
	if err != nil || private != "[]" {
		t.Fatalf("private group search=%q err=%v", private, err)
	}
}

func TestAgentHandlesRouterActivatedGroupRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-group",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"这是公开信息。"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()
	svc, err := New(t.Context(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(t.Context(), Input{
		Text: "介绍一下这个公开服务",
		Identity: store.Identity{
			Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "100",
		},
	})
	if !ok || response.Text != "这是公开信息。" {
		t.Fatalf("response = %#v, ok=%v", response, ok)
	}
}

func TestGroupPersonalDataRequestIsRefusedWithoutCallingModel(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	svc, err := New(t.Context(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(t.Context(), Input{
		Text: "我这学期选了哪些课",
		Identity: store.Identity{
			Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "100",
		},
	})
	if !ok || response.Text != "这个问题涉及个人数据，请私聊 Presto 查询。" {
		t.Fatalf("response=%#v ok=%v", response, ok)
	}
	if requests.Load() != 0 {
		t.Fatalf("model requests=%d want=0", requests.Load())
	}
}

func TestAgentIgnoresBlankMessages(t *testing.T) {
	svc := &Service{enabled: true}
	reply, ok := svc.Handle(context.Background(), Input{
		Text:     " \t\n ",
		Identity: store.Identity{ConversationType: "private"},
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestNewNormalizesModelCredentials(t *testing.T) {
	var gotAuth string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  " test-key ",
		BaseURL: " " + server.URL + "/ ",
		Model:   " test-model ",
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Fatalf("reply = %q", reply.Content)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestNewUsesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 20 * time.Millisecond,
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err == nil || !isTimeoutError(err) {
		t.Fatalf("Generate error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("configured timeout was not used, elapsed = %s", elapsed)
	}
}

func TestNewRetriesTransientChatCompletionTransportError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if attempts.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: time.Second,
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Fatalf("reply = %q", reply.Content)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestAgentToolConstruction(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	mcpURL, mcpHTTPClient, closeMCP, _ := newAgentMCPTestServer(t)
	defer closeMCP()

	svc := &Service{
		handler:   commands.Handler{Store: db},
		auth:      &auth.Manager{Store: db},
		mcpClient: botmcp.New(mcpURL, mcpHTTPClient),
	}
	assertAgentToolNames(t, svc,
		"call_campus_tool",
		"get_current_time",
		"invoke_bot_capability",
		"search_bot_commands",
		"search_campus_tools",
	)
}

func TestLazyMCPSearchAndCallExposeOnlyReadTools(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID: "client", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	mcpURL, mcpHTTPClient, closeMCP, calls := newAgentMCPTestServer(t)
	defer closeMCP()
	svc := &Service{
		handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db},
		mcpClient: botmcp.New(mcpURL, mcpHTTPClient),
	}
	lazy := newLazyMCPSession(svc, ident, 0)
	defer func() { _ = lazy.Close() }()

	docs, err := lazy.search(context.Background(), campusToolSearchInput{Query: "homework"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs, "list_my_homeworks") || strings.Contains(docs, "delete_my_homework") {
		t.Fatalf("read-only MCP docs = %s", docs)
	}
	for _, query := range []string{"第二课堂 活动", "查询第二课堂平台活动", "二课活动"} {
		youngDocs, err := lazy.search(context.Background(), campusToolSearchInput{Query: query})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(youngDocs, "catalog_young_event_list") || !strings.Contains(youngDocs, "catalog_young_event_get") {
			t.Fatalf("young-event MCP docs for %q = %s", query, youngDocs)
		}
	}
	allDocs, err := lazy.search(context.Background(), campusToolSearchInput{Query: "*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"get_current_semester", "list_my_homeworks", "search_courses", "catalog_young_event_list", "catalog_young_event_get"} {
		if !strings.Contains(allDocs, name) {
			t.Fatalf("complete MCP inventory omitted %q: %s", name, allDocs)
		}
	}
	if strings.Contains(allDocs, "delete_my_homework") {
		t.Fatalf("complete MCP inventory exposed mutation: %s", allDocs)
	}
	if _, err := lazy.call(context.Background(), campusToolCallInput{Name: "catalog_young_event_list", Arguments: map[string]any{"active": true}}); err != nil {
		t.Fatal(err)
	}
	if calls["catalog_young_event_list"].Load() != 1 {
		t.Fatal("allowed young-event lookup did not reach the remote server")
	}
	result, err := lazy.call(context.Background(), campusToolCallInput{Name: "search_courses", Arguments: map[string]any{"query": "math"}})
	// The envelope is host scaffolding; the remote body is carried through as
	// JSON rather than buried in an escaped string.
	if err != nil || !strings.Contains(result, `"result":{"ok":true}`) || !strings.Contains(result, `"outcome":"succeeded"`) {
		t.Fatalf("lazy MCP call result=%q err=%v", result, err)
	}
	if _, err := lazy.call(context.Background(), campusToolCallInput{Name: "delete_my_homework"}); err == nil {
		t.Fatal("hidden MCP mutation was callable")
	}
	if calls["delete_my_homework"].Load() != 0 {
		t.Fatal("hidden MCP mutation reached the remote server")
	}

	job, _, err := db.EnqueueConversationJob(context.Background(), store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "lazy-mcp-receipt", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimConversationJob(context.Background(), ident)
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim MCP job=%#v err=%v", claimed, err)
	}
	trackedCtx := store.WithConversationJobLease(context.Background(), job.ID, claimed.LeaseToken)
	tracked := newLazyMCPSession(svc, ident, job.ID)
	defer func() { _ = tracked.Close() }()
	if _, err := tracked.call(trackedCtx, campusToolCallInput{Name: "delete_my_homework", Arguments: map[string]any{"id": 1}}); err == nil {
		t.Fatal("misannotated MCP mutation was callable in a tracked job")
	}
	if executions, err := db.CapabilityExecutionsForJob(context.Background(), job.ID); err != nil || len(executions) != 0 {
		t.Fatalf("hidden MCP mutation created executions=%#v err=%v", executions, err)
	}
	if calls["delete_my_homework"].Load() != 0 {
		t.Fatal("tracked hidden MCP mutation reached the remote server")
	}
	if _, err := tracked.call(trackedCtx, campusToolCallInput{Name: "search_courses", Arguments: map[string]any{"query": "math"}}); err != nil {
		t.Fatal(err)
	}
	executions, err := db.CapabilityExecutionsForJob(context.Background(), job.ID)
	if err != nil || len(executions) != 1 {
		t.Fatalf("MCP executions=%#v err=%v", executions, err)
	}
	if executions[0].State != store.CapabilityExecutionSucceeded || executions[0].Receipt.Action != "查询" ||
		executions[0].Receipt.Resource != "课程" || executions[0].Receipt.Subject != "math" {
		t.Fatalf("MCP execution receipt = %#v", executions[0])
	}
	if ok, err := db.CompleteConversationJob(context.Background(), job.ID, claimed.LeaseToken); err != nil || !ok {
		t.Fatalf("complete first MCP job ok=%v err=%v", ok, err)
	}

	secondJob, _, err := db.EnqueueConversationJob(context.Background(), store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "lazy-mcp-same-lease", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := db.ClaimConversationJob(context.Background(), ident)
	if err != nil || secondClaim == nil || secondClaim.ID != secondJob.ID {
		t.Fatalf("claim second MCP job=%#v err=%v", secondClaim, err)
	}
	secondCtx := store.WithConversationJobLease(context.Background(), secondJob.ID, secondClaim.LeaseToken)
	secondSession := newLazyMCPSession(svc, ident, secondJob.ID)
	defer func() { _ = secondSession.Close() }()
	prepared, trackedExecution, execute, err := secondSession.prepareExecution(secondCtx, "search_courses", map[string]any{"query": "math"})
	if err != nil || !trackedExecution || !execute || prepared.State != store.CapabilityExecutionRunning {
		t.Fatalf("prepare same-lease MCP read=%#v tracked=%v execute=%v err=%v", prepared, trackedExecution, execute, err)
	}
	if _, err := secondSession.call(secondCtx, campusToolCallInput{Name: "search_courses", Arguments: map[string]any{"query": "math"}}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("same-live MCP read was replayed: %v", err)
	}
}

func TestSecondClassroomRequestFallsThroughEmptyBotSearchToLiteralMCPResult(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "young-event", ConversationType: "private", ConversationID: "young-event"}
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	inputText := "现在能查询到二课都有哪些项目了吗"
	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "young-event", Input: store.ConversationJobInput{Text: inputText},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue young-event job: created=%v err=%v", created, err)
	}
	mcpURL, mcpHTTPClient, closeMCP, calls := newAgentMCPTestServer(t)
	t.Cleanup(closeMCP)

	var modelRequests atomic.Int32
	var requestBodies [][]byte
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read model request: %v", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requestBodies = append(requestBodies, body)
		request := modelRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch request {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"young-search-bot","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"young-search-bot-call","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"查询第二课堂平台活动项目列表\"}"}
				}]} ,"finish_reason":"tool_calls"}]
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"young-search-mcp","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"young-search-mcp-call","type":"function","function":{"name":"search_campus_tools","arguments":"{\"query\":\"二课活动 报名\"}"}
				}]} ,"finish_reason":"tool_calls"}]
			}`)
		case 3:
			_, _ = io.WriteString(w, `{
				"id":"young-call-mcp","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"young-call-mcp-call","type":"function","function":{"name":"call_campus_tool","arguments":"{\"name\":\"catalog_young_event_list\",\"arguments\":{\"active\":true,\"page\":1,\"limit\":3}}"}
				}]} ,"finish_reason":"tool_calls"}]
			}`)
		case 4:
			_, _ = io.WriteString(w, `{
				"id":"young-final","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"目前可以报名：第二课堂示例活动，地点为东区图书馆。"},"finish_reason":"stop"}]
			}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(modelServer.Close)
	authManager := &auth.Manager{Store: db}
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model",
		MCPBaseURL: mcpURL, AuthManager: authManager,
	}, commands.Handler{Store: db, Auth: authManager}, mcpHTTPClient)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.Run(ctx, claimAgentInput(t, db, ident, Input{Text: inputText, Identity: ident, JobID: job.ID}))
	if !result.Handled || result.State != RunStateCompleted || result.Response.Text != "目前可以报名：第二课堂示例活动，地点为东区图书馆。" {
		t.Fatalf("young-event result = %#v", result)
	}
	if got := modelRequests.Load(); got != 4 {
		t.Fatalf("model requests = %d, want 4", got)
	}
	if calls["catalog_young_event_list"].Load() != 1 {
		t.Fatalf("young-event MCP calls = %d, want 1", calls["catalog_young_event_list"].Load())
	}
	if len(requestBodies) != 4 || !bytes.Contains(requestBodies[1], []byte(`"content":"[]"`)) ||
		!bytes.Contains(requestBodies[2], []byte("catalog_young_event_list")) ||
		!bytes.Contains(requestBodies[3], []byte("youngId")) || !bytes.Contains(requestBodies[3], []byte("第二课堂示例活动")) {
		t.Fatalf("model did not receive the exact empty-search/docs/result sequence: %q", requestBodies)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].Capability != "mcp:catalog_young_event_list" ||
		executions[0].State != store.CapabilityExecutionSucceeded || executions[0].Effect != string(commands.EffectRead) {
		t.Fatalf("young-event execution = %#v err=%v", executions, err)
	}
}

func TestAgentToolConstructionSkipsUnavailableCommandTools(t *testing.T) {
	assertAgentToolNames(t, &Service{},
		"get_current_time",
		"invoke_bot_capability",
		"search_bot_commands",
	)
}

func TestAgentToolConstructionKeepsStoreOnlyCommandTools(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	assertAgentToolNames(t, &Service{handler: commands.Handler{Store: db}},
		"get_current_time",
		"invoke_bot_capability",
		"search_bot_commands",
	)
}

func TestToolsForKeepsHostCapabilitiesWhenMCPTokenMissing(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", UserCode: "ABCD", VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		handler:   commands.Handler{Store: db, Auth: &auth.Manager{Store: db}},
		auth:      &auth.Manager{Store: db},
		mcpClient: botmcp.New("http://127.0.0.1:1/api/mcp", http.DefaultClient),
	}
	var logs bytes.Buffer
	svc.logger = log.New(&logs, "", 0)
	names := agentToolNames(t, svc)
	if !names["invoke_bot_capability"] {
		t.Fatalf("host capability tool is missing: %#v", names)
	}
	if !names["search_campus_tools"] || logs.Len() != 0 {
		t.Fatalf("MCP was initialized before a tool request: names=%#v logs=%q", names, logs.String())
	}
	session := newLazyMCPSession(svc, ident, 0)
	if _, err := session.search(context.Background(), campusToolSearchInput{Query: "course"}); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("lazy MCP search error = %v", err)
	}
}

func TestToolsForKeepsHostCapabilitiesWhenMCPResourceIsNotApproved(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": serverURL, "token_endpoint": serverURL + "/token",
			})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"resource not approved"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID: "client", AccessToken: "expired", RefreshToken: "refresh",
		ExpiresAt: time.Now().Add(-time.Hour), Resource: server.URL + " " + server.URL + "/api/mcp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", UserCode: "ABCD", VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := &Service{
		enabled:   true,
		handler:   commands.Handler{Store: db, Auth: &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db}},
		auth:      &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		mcpClient: botmcp.New(server.URL+"/api/mcp", server.Client()),
		logger:    log.New(&logs, "", 0),
	}

	names := agentToolNames(t, svc)
	if !names["invoke_bot_capability"] {
		t.Fatalf("host capability tool is missing: %#v", names)
	}
	if logs.Len() != 0 {
		t.Fatalf("MCP initialized while only constructing tools: %q", logs.String())
	}
	session := newLazyMCPSession(svc, ident, 0)
	if _, err := session.search(context.Background(), campusToolSearchInput{Query: "course"}); err == nil {
		t.Fatal("lazy MCP search unexpectedly succeeded with an unapproved resource")
	}
	credential, err := db.Credential(context.Background(), ident)
	if err != nil || credential != nil {
		t.Fatalf("credential = %#v, err = %v; want deleted", credential, err)
	}
}

func TestMCPAuthorizationFailureDoesNotLoopReauthorizationForCurrentScopes(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	currentScopes := strings.Join([]string{
		"openid", "profile", "email", "offline_access", "account.profile:read", "account.client-activity:read",
		"workspace.todo:read", "workspace.todo:write", "workspace.homework:read", "workspace.homework:write",
		"workspace.subscription:read", "workspace.subscription:write", "workspace.calendar-feed:read", "workspace.calendar:read",
		"community.comment:read", "community.comment:write", "community.description:read", "community.description:write",
		"community.user:read", "community.section-homework:read", "community.section-homework:write",
		"workspace.upload:read", "workspace.upload:write", "workspace.overview:read", "workspace.link-pin:read", "workspace.link-pin:write",
		"catalog.bus:read", "workspace.bus-preferences:read", "workspace.bus-preferences:write",
		"catalog.course:read", "catalog.section:read", "catalog.teacher:read", "catalog.schedule:read", "workspace.schedule:read",
		"catalog.exam:read", "workspace.exam:read",
	}, " ")
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Scope: currentScopes,
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{auth: &auth.Manager{Store: db}}
	reply := svc.mcpFailureReply(ctx, ident, 0, transport.ErrAuthorizationRequired)
	if strings.Contains(reply, "请发送：登录") || !strings.Contains(reply, "请稍后重试") {
		t.Fatalf("reply = %q", reply)
	}
	if credential, err := db.Credential(ctx, ident); err != nil || credential == nil {
		t.Fatalf("credential = %#v, err = %v; want preserved", credential, err)
	}
}

func TestMCPReauthorizationReplyDoesNotClearCredentialsOrStartLogin(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Scope: "old:scope",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{auth: &auth.Manager{Store: db}}
	reply := svc.mcpFailureReply(ctx, ident, 0, auth.ErrReauthorizationRequired)
	if !strings.Contains(reply, "请直接发送“登录”重新授权") {
		t.Fatalf("reply = %q", reply)
	}
	credential, err := db.Credential(ctx, ident)
	if err != nil || credential == nil || credential.AccessToken != "access" {
		t.Fatalf("Agent authorization error changed credential=%#v err=%v", credential, err)
	}
	session, err := db.ActiveLoginSession(ctx, ident)
	if err != nil || session != nil {
		t.Fatalf("Agent authorization error started login=%#v err=%v", session, err)
	}
}

func agentToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	tools, session, err := svc.toolsFor(context.Background(), store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		defer func() { _ = session.Close() }()
	}
	names := map[string]bool{}
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if names[info.Name] {
			t.Fatalf("duplicate tool %q", info.Name)
		}
		names[info.Name] = true
	}
	return names
}

func newAgentMCPTestServer(t *testing.T) (string, *http.Client, func(), map[string]*atomic.Int32) {
	t.Helper()
	mcpServer := mcpserver.NewMCPServer("agent-test", "1.0.0")
	calls := map[string]*atomic.Int32{}
	for _, tool := range []mcpgo.Tool{
		mcpgo.NewTool("list_my_homeworks", mcpgo.WithDescription("List my homeworks.")),
		mcpgo.NewTool("search_courses", mcpgo.WithDescription("Search courses.")),
		mcpgo.NewTool("get_current_semester", mcpgo.WithDescription("Get current semester.")),
		mcpgo.NewTool("catalog_young_event_list", mcpgo.WithDescription("List second-classroom (第二课堂) signup events.")),
		mcpgo.NewTool("catalog_young_event_get", mcpgo.WithDescription("Fetch one second-classroom (第二课堂) signup event.")),
		mcpgo.NewTool("delete_my_homework", mcpgo.WithDescription("Delete a homework.")),
	} {
		tool := tool
		readOnly := true
		tool.Annotations.ReadOnlyHint = &readOnly
		calls[tool.Name] = &atomic.Int32{}
		mcpServer.AddTool(tool, func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			calls[tool.Name].Add(1)
			result := `{"ok":true}`
			if tool.Name == "catalog_young_event_list" {
				result = `{"data":[{"youngId":"event-1","name":"第二课堂示例活动","category":"单次项目","status":"报名中","isActive":true,"location":"东区图书馆"}],"pagination":{"page":1,"pageSize":20,"total":1}}`
			}
			return mcpgo.NewToolResultText(result), nil
		})
	}
	handler := mcpserver.NewStreamableHTTPServer(mcpServer)
	server := httptest.NewServer(handler)
	return server.URL, server.Client(), server.Close, calls
}

func assertAgentToolNames(t *testing.T, svc *Service, wantNames ...string) {
	t.Helper()
	names := agentToolNames(t, svc)
	if len(names) != len(wantNames) {
		t.Fatalf("tool count = %d, want %d; tools = %#v", len(names), len(wantNames), names)
	}
	seen := map[string]bool{}
	for _, name := range wantNames {
		if seen[name] {
			t.Fatalf("duplicate expected tool %q", name)
		}
		seen[name] = true
		if !names[name] {
			t.Fatalf("missing tool %q; tools = %#v", name, names)
		}
	}
}

func TestToolResultMiddlewarePropagatesErrors(t *testing.T) {
	outcomes := newToolOutcomeRegistry()
	ctx := withToolOutcomes(context.Background(), outcomes)
	input := &compose.ToolInput{Name: "test_tool", Arguments: "{}", CallID: "call-1"}
	var logs bytes.Buffer
	logf := log.New(&logs, "", 0).Printf

	failing := toolResultMiddleware(logf)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, errors.New("bad args")
	})
	out, err := failing(ctx, input)
	if err == nil || out != nil || !strings.Contains(err.Error(), "bad args") {
		t.Fatalf("result = %#v, err = %v", out, err)
	}
	if !strings.Contains(logs.String(), "bad args") || !strings.Contains(logs.String(), "test_tool") {
		t.Fatalf("logs = %q", logs.String())
	}

	recoverableErr := recoverableMCPToolError(t)
	recoverable := toolResultMiddleware(logf)(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, recoverableErr
	})
	out, err = recoverable(ctx, input)
	if err != nil || out == nil || out.Result != "semesterJwId must be greater than 0" {
		t.Fatalf("recoverable result = %#v, err = %v", out, err)
	}
	if !outcomes.isError(input.CallID) {
		t.Fatal("recoverable MCP failure was not typed as a tool error")
	}

	ok := toolResultMiddleware(nil)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	out, err = ok(ctx, input)
	if err != nil || out.Result != "ok" {
		t.Fatalf("ok result = %q, err = %v", out.Result, err)
	}
}

func recoverableMCPToolError(t *testing.T) error {
	t.Helper()
	mcpServer := mcpserver.NewMCPServer("agent-test", "1.0.0")
	mcpServer.AddTool(mcpgo.NewTool("search_courses"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultError("semesterJwId must be greater than 0"), nil
	})
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	t.Cleanup(server.Close)
	session, err := botmcp.New(server.URL, server.Client()).OpenSession(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	_, err = session.Call(context.Background(), "search_courses", nil)
	if err == nil {
		t.Fatal("MCP error result was not classified")
	}
	return err
}

func TestCleanQQReplyRemovesMarkdownTables(t *testing.T) {
	input := `---

## 明天行程
| 时间 | 事项 | 地点 |
|------|------|------|
| **07:50-09:25** | 组合数学 | 高新区 **GT-B112** |

> 建议下课后出发
详情见 [校车](https://example.test/bus)`
	got := cleanQQReply(input)
	want := "明天行程\n时间  事项  地点\n07:50-09:25  组合数学  高新区 GT-B112\n\n建议下课后出发\n详情见 校车"
	if got != want {
		t.Fatalf("cleanQQReply = %q", got)
	}
}

func TestCurrentTimeHelpersUseShanghaiTime(t *testing.T) {
	now := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	if got := currentTimeMessageAt(now); got != "现在是 2026-06-07 18:30，Asia/Shanghai。" {
		t.Fatalf("currentTimeMessageAt = %q", got)
	}
	instruction := currentInstructionAt(now)
	if !strings.HasPrefix(instruction, "You are Presto,") || strings.Contains(strings.ToLower(instruction), "signal_bot") {
		t.Fatalf("instruction identity = %q", instruction)
	}
	if strings.Contains(instruction, "Current local time is") {
		t.Fatalf("instruction should not embed wall-clock time (cache stability): %q", instruction)
	}
	if !strings.Contains(instruction, "Avoid emojis") {
		t.Fatalf("instruction = %q", instruction)
	}
	for _, leaked := range []string{"opaque host behavior", "pauses and resumes", "approval"} {
		if strings.Contains(strings.ToLower(instruction), leaked) {
			t.Fatalf("instruction leaks host authorization mechanics %q: %q", leaked, instruction)
		}
	}
	if !strings.Contains(instruction, "Never invent prices, menus, locations, schedules, bus times, service availability, personal data, or operation results") ||
		!strings.Contains(instruction, "A tool result carries the time it was produced") ||
		!strings.Contains(instruction, "unless a domain tool actually returned that evidence") {
		t.Fatalf("instruction lacks the evidence rule: %q", instruction)
	}
	if !strings.Contains(instruction, "preserving every user constraint") || !strings.Contains(instruction, "dates, times, filters, targets, and direction") {
		t.Fatalf("instruction lacks universal argument-preservation rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never use Markdown tables") {
		t.Fatalf("instruction lacks QQ plain-text rule: %q", instruction)
	}
	if !strings.Contains(instruction, "search_bot_commands") || !strings.Contains(instruction, "invoke_bot_capability") || !strings.Contains(instruction, "literal evidence") {
		t.Fatalf("instruction lacks capability workflow: %q", instruction)
	}
	if !strings.Contains(instruction, "Private URLs returned by a tool may be used and repeated in a direct chat") {
		t.Fatalf("instruction lacks private URL policy: %q", instruction)
	}
	for _, obsolete := range []string{"execute_bot_command", "resolve_image_command", "![]("} {
		if strings.Contains(instruction, obsolete) {
			t.Fatalf("instruction retained obsolete protocol %q: %q", obsolete, instruction)
		}
	}
}

func TestHostCapabilityToolReturnsPrivateCalendarURLInPrivateModelContext(t *testing.T) {
	ctx := context.Background()
	calendarURL := "https://life.example/api/calendar-feeds/user-1:private-token.ics"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"subscription":{"calendarUrl":%q}}`, calendarURL)
	}))
	t.Cleanup(server.Close)

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	authManager := &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db}
	svc := &Service{handler: commands.Handler{
		Life: life.NewClient(server.URL, server.Client()), Auth: authManager, Store: db,
	}, auth: authManager}

	var delivered commands.Response
	tools, session, err := svc.toolsFor(ctx, ident, 0, func(_ context.Context, _ store.Identity, response commands.Response) error {
		delivered = response
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	var hostTool einotool.InvokableTool
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "invoke_bot_capability" {
			var ok bool
			hostTool, ok = candidate.(einotool.InvokableTool)
			if !ok {
				t.Fatalf("host capability tool is not invokable: %T", candidate)
			}
			break
		}
	}
	if hostTool == nil {
		t.Fatal("invoke_bot_capability tool is missing")
	}
	result, err := hostTool.InvokableRun(ctx, `{"capability":"subscription","arguments":["link"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Text != "" || delivered.Image != nil || len(delivered.Parts) != 0 {
		t.Fatalf("private URL was redundantly delivered by host = %#v", delivered)
	}
	if !strings.Contains(result, calendarURL) || !strings.Contains(result, "通过 URL 添加/订阅日历") {
		t.Fatalf("model-facing tool result = %q", result)
	}
}

func TestHostReadCapabilityReportsLoginRequirementWithoutHostSideEffect(t *testing.T) {
	ctx := context.Background()
	const userCode = "ABCD-SECRET"
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode: "device", UserCode: userCode, VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	authManager := &auth.Manager{Store: db}
	svc := &Service{handler: commands.Handler{
		Life: life.NewClient("http://life.invalid", nil), Auth: authManager, Store: db,
	}, auth: authManager}

	var delivered commands.Response
	tools, session, err := svc.toolsFor(ctx, ident, 0, func(_ context.Context, _ store.Identity, response commands.Response) error {
		delivered = response
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	var hostTool einotool.InvokableTool
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "invoke_bot_capability" {
			var ok bool
			hostTool, ok = candidate.(einotool.InvokableTool)
			if !ok {
				t.Fatalf("host capability tool is not invokable: %T", candidate)
			}
			break
		}
	}
	if hostTool == nil {
		t.Fatal("invoke_bot_capability tool is missing")
	}
	result, err := hostTool.InvokableRun(ctx, `{"capability":"schedule","arguments":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Text != "" || delivered.Kind != "" {
		t.Fatalf("Agent read unexpectedly delivered a host login response = %#v", delivered)
	}
	if strings.Contains(result, userCode) || strings.Contains(result, "login.example") || !strings.Contains(result, "请直接发送“登录”") {
		t.Fatalf("model-facing auth result = %q", result)
	}
}

func TestHostCapabilityDescriptionIsSmallAndCommandSearchReturnsExactCalls(t *testing.T) {
	description := hostCapabilityToolDescription(false)
	// The envelope shape is part of the contract the model is handed, but the
	// registry itself must stay out of the description.
	for _, expected := range []string{"envelope", "outcome", "observed_at", "result"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("description lacks %q: %q", expected, description)
		}
	}
	if strings.Contains(description, "invalid_input") || strings.Contains(description, "ok:false") {
		t.Fatalf("description retained eager registry/status protocol: %q", description)
	}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	subscription, err := searchCommandDocumentation(ident, commandSearchInput{Query: "subscription"})
	if err != nil || !strings.Contains(subscription, `"capability":"subscription","arguments":["link"]`) {
		t.Fatalf("subscription documentation=%q err=%v", subscription, err)
	}
	if strings.Contains(subscription, "confirmation") || strings.Contains(strings.ToLower(description), "confirm") || strings.Contains(strings.ToLower(description), "approval") {
		t.Fatalf("confirmation mechanics leaked to model: description=%q documentation=%q", description, subscription)
	}
	bus, err := searchCommandDocumentation(ident, commandSearchInput{Query: "bus"})
	if err != nil || !strings.Contains(bus, `"capability":"bus","arguments":["2026-09-06","东区","太湖路园区"]`) {
		t.Fatalf("bus documentation=%q err=%v", bus, err)
	}
	selectedCourses, err := searchCommandDocumentation(ident, commandSearchInput{Query: "查询本学期已选课程 选课列表 课程表"})
	if err != nil || !strings.Contains(selectedCourses, `"id":"my_subscribed_sections"`) || strings.Contains(selectedCourses, `"id":"semester"`) {
		t.Fatalf("selected-course documentation=%q err=%v", selectedCourses, err)
	}
}

func TestCommandSearchRejectsBlankQueryWithActionableToolResult(t *testing.T) {
	_, err := searchCommandDocumentation(store.Identity{ConversationType: "private"}, commandSearchInput{})
	result, recoverable := botmcp.ModelToolErrorResult(err)
	if !recoverable || !strings.Contains(result, "本学期已选课程") || !strings.Contains(result, "明天课表") {
		t.Fatalf("blank command search result=%q recoverable=%v err=%v", result, recoverable, err)
	}
}

func TestMessagesForIncludesTypedHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for _, event := range []store.ConversationEvent{
		{Identity: ident, DedupeKey: "typed:user", Type: store.ConversationEventUser, Content: "你好"},
		{Identity: ident, DedupeKey: "typed:assistant", Type: store.ConversationEventAssistant, Content: "你好！\n有什么可以帮你的吗？"},
	} {
		if _, _, err := db.AppendConversationEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	messages, err := svc.messagesFor(ctx, Input{Text: "我上面说了什么？", Identity: ident})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Content != "你好" ||
		messages[1].Content != "你好！\n有什么可以帮你的吗？" ||
		!strings.Contains(messages[2].Content, "我上面说了什么？") ||
		!strings.HasPrefix(messages[2].Content, "现在是 ") {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestMessagesForDoesNotDuplicatePersistedCurrentJobEvent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "current", ConversationType: "private", ConversationID: "current"}
	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "current-event", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: job=%#v created=%v err=%v", job, created, err)
	}
	claimed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim: job=%#v err=%v", claimed, err)
	}
	event := store.ConversationEvent{
		Identity: ident, JobID: job.ID, JobRevision: claimed.Revision, JobLeaseToken: claimed.LeaseToken,
		DedupeKey: fmt.Sprintf("conversation-job:%d:user", job.ID),
		Type:      store.ConversationEventUser, Content: "同一个问题",
		Parts: []store.ConversationMessagePart{{Type: "text", Text: "现在是 2026-09-02 12:00，Asia/Shanghai。\n\n同一个问题"}},
	}
	if _, _, err := db.AppendConversationEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	messages, err := svc.messagesFor(ctx, Input{Text: "同一个问题", Identity: ident, JobID: job.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].UserInputMultiContent) != 1 || messages[0].UserInputMultiContent[0].Text != event.Parts[0].Text {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestHandleResponseUsesTypedTranscriptWithoutSummaryRequest(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for _, event := range []store.ConversationEvent{
		{Identity: ident, DedupeKey: "history:user", Type: store.ConversationEventUser, Content: "查数学分析"},
		{Identity: ident, DedupeKey: "history:assistant-call", Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{ID: "call-history", Name: "invoke_bot_capability", Arguments: `{"capability":"course_search","arguments":["数学分析"]}`}}},
		{Identity: ident, DedupeKey: "history:tool", Type: store.ConversationEventToolResult, ToolCallID: "call-history", ToolName: "invoke_bot_capability", Content: "数学分析（程艺，2026春）"},
		{Identity: ident, DedupeKey: "history:assistant", Type: store.ConversationEventAssistant, Content: "程艺老师在 2026 春开课。"},
	} {
		if _, _, err := db.AppendConversationEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if requests.Add(1) != 1 {
			t.Fatalf("unexpected extra model request")
		}
		for _, want := range [][]byte{[]byte("call-history"), []byte("course_search"), []byte("数学分析（程艺，2026春）"), []byte("continue")} {
			if !bytes.Contains(body, want) {
				t.Fatalf("typed history missing %q: %s", want, body)
			}
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-answer",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"continued"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
		}`))
	}))
	defer server.Close()
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{Text: "continue", Identity: ident})
	if !ok || response.Text != "continued" || requests.Load() != 1 {
		t.Fatalf("response=%#v ok=%v requests=%d", response, ok, requests.Load())
	}
}

func TestHandleResponseDropsOrphanedToolCallBeforeNewConversationTurn(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "orphan-history", ConversationType: "private", ConversationID: "orphan-history"}
	for _, event := range []store.ConversationEvent{
		{Identity: ident, DedupeKey: "orphan:user", Type: store.ConversationEventUser, Content: "执行之前的操作"},
		{Identity: ident, DedupeKey: "orphan:assistant", Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{
			ID: "orphaned-tool-call", Name: "invoke_bot_capability", Arguments: `{"capability":"notify","arguments":["homework","on"]}`,
		}}},
	} {
		if _, _, err := db.AppendConversationEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte("orphaned-tool-call")) {
			t.Fatalf("provider request retained malformed historical turn: %s", body)
		}
		if !bytes.Contains(body, []byte("你还在吗")) {
			t.Fatalf("provider request lost current user turn: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-recovered-history","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"在的。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
		}`))
	}))
	defer server.Close()
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{Text: "你还在吗", Identity: ident})
	if !ok || response.Text != "在的。" {
		t.Fatalf("response=%#v ok=%v", response, ok)
	}
}

func TestHandleResponseRejectsHardLimitImageBeforeModelCall(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "admin", ConversationType: "private", ConversationID: "admin"}
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxImageDownloadBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer imageServer.Close()
	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer modelServer.Close()
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "default", BaseURL: modelServer.URL, Model: "default",
		PremiumAPIKey: "premium", PremiumBaseURL: modelServer.URL, PremiumModel: "premium",
	}, commands.Handler{Store: db}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{
		Text: "看图", ImageURLs: []string{imageServer.URL + "/too-large.png"}, Identity: ident,
	})
	if !ok || !strings.Contains(response.Text, "AI 图片处理失败：图片超过 25 MiB 安全上限") {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if modelRequests.Load() != 0 {
		t.Fatalf("model requests before image validation = %d", modelRequests.Load())
	}
}

func TestHandleResponseStopsAtModelIterationLimit(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-loop",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call-loop",
						"type":"function",
						"function":{"name":"get_current_time","arguments":"{}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{Text: "loop", Identity: ident})
	if !ok {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	// The first identical calls are refused inside the tool channel so the model
	// can change plan; a model that keeps repeating exhausts the refusal budget
	// and the run still stops with the same user-visible message.
	if !strings.Contains(response.Text, "重复调用了相同工具") {
		t.Fatalf("response = %#v", response)
	}
	if got := int(requests.Load()); got < 2 || got > maxRepeatRefusals+2 {
		t.Fatalf("model requests = %d, want between 2 and %d", got, maxRepeatRefusals+2)
	}
	total, err := db.ConversationSpending(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	// One admitted tool call plus the bounded refusals; the durable spending row
	// must stay bounded rather than running to the framework iteration cap.
	if total.ToolCalls != 1 || total.ModelRequests < 2 || total.ModelRequests > int64(maxRepeatRefusals)+2 {
		t.Fatalf("spending = %#v", total)
	}
}

func TestHandleResponseDoesNotStartLoginForAgentRead(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var authServerURL string
	authMux := http.NewServeMux()
	authMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": authServerURL + "/device",
			"token_endpoint":                authServerURL + "/token",
			"registration_endpoint":         authServerURL + "/register",
		})
	})
	authMux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	var deviceRequests atomic.Int32
	authMux.HandleFunc("/device", func(w http.ResponseWriter, _ *http.Request) {
		deviceRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "device", "user_code": "USER-CODE",
			"verification_uri": authServerURL + "/verify", "expires_in": 300, "interval": 5,
		})
	})
	authServer := httptest.NewServer(authMux)
	defer authServer.Close()
	authServerURL = authServer.URL
	manager := &auth.Manager{Server: authServer.URL, HTTPClient: authServer.Client(), Store: db}

	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		request := modelRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-host-tool","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-host","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"schedule\",\"arguments\":[]}"}
				}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-empty","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"需要登录，请直接发送登录后重试。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}
		}`))
	}))
	defer modelServer.Close()

	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model",
	}, commands.Handler{Life: life.NewClient("http://life.invalid", nil), Auth: manager, Store: db}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var deliveries atomic.Int32
	response, ok := svc.HandleResponse(ctx, Input{
		Text: "你好呀", Identity: ident,
		SendResponse: func(_ context.Context, got store.Identity, delivered commands.Response) error {
			t.Fatalf("Agent read started host login: identity=%#v response=%#v", got, delivered)
			deliveries.Add(1)
			return nil
		},
	})
	if !ok || !strings.Contains(response.Text, "直接发送登录") || response.Kind != "agent" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if got := deliveries.Load(); got != 0 {
		t.Fatalf("host deliveries = %d, want 0", got)
	}
	if got := modelRequests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
	if got := deviceRequests.Load(); got != 0 {
		t.Fatalf("device login requests = %d, want 0", got)
	}
}

func TestRunExecutesAgentReadWithoutConfirmation(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "agent-read-no-confirmation", Input: store.ConversationJobInput{Text: "hello"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue job: created=%v err=%v", created, err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-read","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-read","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"help\",\"arguments\":[]}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-read-done","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"服务状态已返回。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":6,"completion_tokens":2,"total_tokens":8}
		}`))
	}))
	t.Cleanup(server.Close)
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "hello", Identity: ident, JobID: job.ID})
	result := svc.Run(ctx, input)
	if !result.Handled || result.State != RunStateCompleted || result.Response.Text != "服务状态已返回。" {
		t.Fatalf("result = %#v", result)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].Capability != string(commands.CapabilityHelp) ||
		executions[0].Effect != string(commands.EffectRead) || executions[0].State != store.CapabilityExecutionSucceeded ||
		executions[0].ConfirmedAt != nil {
		t.Fatalf("read executions=%#v err=%v", executions, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests=%d want=2", requests.Load())
	}
}

func TestApprovedAgentLoginStartsOnlyAfterConfirmation(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}

	var authServerURL string
	var deviceRequests atomic.Int32
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": authServerURL, "device_authorization_endpoint": authServerURL + "/device",
				"token_endpoint": authServerURL + "/token", "registration_endpoint": authServerURL + "/register",
			})
		case "/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
		case "/device":
			deviceRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "device", "user_code": "USER-CODE", "verification_uri": authServerURL + "/verify",
				"expires_in": 300, "interval": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(authServer.Close)
	authServerURL = authServer.URL
	manager := &auth.Manager{Server: authServer.URL, HTTPClient: authServer.Client(), Store: db}
	svc := &Service{handler: commands.Handler{Auth: manager, Store: db}, auth: manager}

	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "agent-login-confirmation", Input: store.ConversationJobInput{Text: "帮我登录"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue login job=%#v created=%v err=%v", job, created, err)
	}
	claimed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim login job=%#v err=%v", claimed, err)
	}
	execution, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: claimed.LeaseToken,
		DedupeKey: "agent-login-confirmation", Capability: string(commands.CapabilityLogin), Effect: string(commands.EffectWrite),
		Receipt: store.CapabilityReceipt{Action: "登录", Resource: "账户", Subject: "当前账户"}, RequiresConfirmation: true,
	})
	if err != nil || !created || execution.State != store.CapabilityExecutionAwaitingConfirmation || deviceRequests.Load() != 0 {
		t.Fatalf("prepared login=%#v created=%v device_requests=%d err=%v", execution, created, deviceRequests.Load(), err)
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, claimed.LeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause login confirmation ok=%v err=%v", ok, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true}); err != nil || released == nil {
		t.Fatalf("approve login released=%#v err=%v", released, err)
	}
	resumed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || resumed == nil {
		t.Fatalf("claim approved login job=%#v err=%v", resumed, err)
	}
	running, execute, err := db.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, resumed.LeaseToken)
	if err != nil || !execute || running.State != store.CapabilityExecutionRunning {
		t.Fatalf("claim approved login=%#v execute=%v err=%v", running, execute, err)
	}
	var delivered commands.Response
	deferred, waitingAuth, err := svc.executeApprovedCapability(ctx, running, ident, func(_ context.Context, got store.Identity, response commands.Response) error {
		if got != ident {
			t.Fatalf("login delivery identity=%#v", got)
		}
		delivered = response
		return nil
	})
	if err != nil || !waitingAuth || deferred.State != store.CapabilityExecutionWaitingAuth {
		t.Fatalf("approved login result=%#v waiting_auth=%v err=%v", deferred, waitingAuth, err)
	}
	if deviceRequests.Load() != 1 || delivered.Kind != commands.ResponseKindAuthWait || !strings.Contains(delivered.Text, "USER-CODE") {
		t.Fatalf("approved login device_requests=%d response=%#v", deviceRequests.Load(), delivered)
	}
}

// destructiveHandlerForTest wires 订阅 移除, the Store+Life capability the
// registry classifies as destructive, so the confirmation machinery can be
// exercised independently of any particular ordinary write. Ordinary writes no
// longer confirm: the server's scope registry decides whether a user may write,
// and the confirmation gate now exists only for destructive consent.
func destructiveHandlerForTest(t *testing.T, ctx context.Context, db *store.Store, ident store.Identity) (commands.Handler, *atomic.Int32) {
	t.Helper()
	removals := &atomic.Int32{}
	lifeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"id":42,"jwId":202602,"nameCn":"2026年秋季学期"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/catalog/sections":
			code := r.URL.Query().Get("search")
			_, _ = w.Write([]byte(`{"data":[{"id":12,"code":"` + code + `","course":{"namePrimary":"编译原理"},"teacher":{"namePrimary":"程老师"},"semester":{"nameCn":"2026年秋季学期"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspace/subscriptions/batch":
			removals.Add(1)
			var body struct {
				Codes []string `json:"codes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"semester":{"nameCn":"2026年秋季学期"},"removedCount":1,"unchangedCount":0,"sections":[{"code":"` + strings.Join(body.Codes, ",") + `","course":{"namePrimary":"编译原理"},"semester":{"nameCn":"2026年秋季学期"}}]}`))
		default:
			t.Errorf("unexpected Life request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(lifeServer.Close)
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: lifeServer.URL,
	}); err != nil {
		t.Fatal(err)
	}
	return commands.Handler{
		Life:  life.NewClient(lifeServer.URL, lifeServer.Client()),
		Auth:  &auth.Manager{Server: lifeServer.URL, HTTPClient: lifeServer.Client(), Store: db},
		Store: db,
	}, removals
}

func TestRunPausesForHostConfirmationAndResumesExactToolTranscript(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	handler, removals := destructiveHandlerForTest(t, ctx, db, ident)
	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "checkpoint-confirm", Input: store.ConversationJobInput{Text: "退订 COMP6212P.02"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue job: created=%v err=%v", created, err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-confirm-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-confirm","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消订阅课程\"}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}
			}`))
			return
		}
		if request == 2 {
			if !bytes.Contains(body, []byte(`\"id\":\"subscription\"`)) {
				t.Errorf("capability request lacks command-search result: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-confirm","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-confirm","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
			}`))
			return
		}
		if request != 3 {
			t.Errorf("unexpected model request %d", request)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !bytes.Contains(body, []byte("call-confirm")) || !bytes.Contains(body, []byte("COMP6212P.02")) {
			t.Errorf("resumed model request lacks exact tool result: %s", body)
		}
		if bytes.Contains(body, []byte(`"content":"ok"`)) || bytes.Contains(body, []byte(`"content":"确认"`)) {
			t.Errorf("host approval leaked into model transcript: %s", body)
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-done","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"已处理。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}
		}`))
	}))
	defer server.Close()

	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	firstInput := claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	first := svc.Run(ctx, firstInput)
	if !first.Handled || first.State != RunStateInterrupted || first.Response.Text != "" {
		t.Fatalf("first run = %#v", first)
	}
	if got := removals.Load(); got != 0 {
		t.Fatalf("mutation ran before approval: removals=%d", got)
	}
	operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 1 || operations[0].State != store.CapabilityExecutionAwaitingConfirmation {
		t.Fatalf("pending operations=%#v err=%v", operations, err)
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, firstInput.JobLeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause confirmation job: ok=%v err=%v", ok, err)
	}
	if _, found, err := db.AgentCheckpoints().Get(ctx, agentCheckpointID(job.ID)); err != nil || !found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true}); err != nil || released == nil {
		t.Fatalf("approve operation: released=%#v err=%v", released, err)
	}

	secondInput := claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	second := svc.Run(ctx, secondInput)
	if !second.Handled || second.State != RunStateCompleted || second.Response.Text != "已处理。" {
		t.Fatalf("second run = %#v", second)
	}
	if got := removals.Load(); got != 1 {
		t.Fatalf("approved mutation: removals=%d", got)
	}
	operations, err = db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 1 || operations[0].State != store.CapabilityExecutionSucceeded ||
		!strings.Contains(operations[0].Result, "编译原理") {
		t.Fatalf("completed operations=%#v err=%v", operations, err)
	}
	if _, found, err := db.AgentCheckpoints().Get(ctx, agentCheckpointID(job.ID)); err != nil || !found {
		t.Fatalf("checkpoint must survive until coordinator acknowledgement: found=%v err=%v", found, err)
	}
	if ok, err := db.CompleteConversationJob(ctx, job.ID, secondInput.JobLeaseToken); err != nil || !ok {
		t.Fatalf("complete resumed job: ok=%v err=%v", ok, err)
	}
	if err := svc.Acknowledge(ctx, job.ID, secondInput.JobRevision, secondInput.JobLeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.AgentCheckpoints().Get(ctx, agentCheckpointID(job.ID)); err != nil || found {
		t.Fatalf("acknowledged checkpoint found=%v err=%v", found, err)
	}
	events, err := db.RecentConversationEvents(ctx, ident, 20)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []store.ConversationEventType{
		store.ConversationEventUser,
		store.ConversationEventAssistant, store.ConversationEventToolResult,
		store.ConversationEventAssistant, store.ConversationEventToolResult,
		store.ConversationEventAssistant,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("typed events = %#v", events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type=%q want=%q events=%#v", index, events[index].Type, want, events)
		}
		if events[index].Content == "ok" || events[index].Content == "确认" {
			t.Fatalf("approval persisted in model transcript: %#v", events[index])
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestRunDiscoversCodeBasedUnsubscribeAndExecutesOnlyAfterConfirmation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "unsubscribe", ConversationType: "private", ConversationID: "unsubscribe"}
	job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "unsubscribe", Input: store.ConversationJobInput{Text: "取消 COMP6212P.02 的课程订阅"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue job=%#v created=%v err=%v", job, created, err)
	}

	var removeCalls atomic.Int32
	lifeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"id":42,"jwId":202602,"nameCn":"2026年秋季学期"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/catalog/sections":
			if r.URL.Query().Get("search") != "COMP6212P.02" || r.URL.Query().Get("semesterId") != "42" {
				t.Errorf("section preflight query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{
				"id":12,"code":"COMP6212P.02","course":{"namePrimary":"编译原理"},
				"teacher":{"namePrimary":"程老师"},"semester":{"nameCn":"2026年秋季学期"}
			}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspace/subscriptions/batch":
			removeCalls.Add(1)
			var body struct {
				Action     string   `json:"action"`
				Codes      []string `json:"codes"`
				SemesterID string   `json:"semesterId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Action != "remove" || strings.Join(body.Codes, ",") != "COMP6212P.02" || body.SemesterID != "42" {
				t.Errorf("unsubscribe body=%#v", body)
			}
			_, _ = w.Write([]byte(`{
				"semester":{"nameCn":"2026年秋季学期"},"removedCount":1,"unchangedCount":0,
				"sections":[{"code":"COMP6212P.02","course":{"namePrimary":"编译原理"},"semester":{"nameCn":"2026年秋季学期"}}]
			}`))
		default:
			t.Errorf("unexpected Life request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer lifeServer.Close()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: lifeServer.URL,
	}); err != nil {
		t.Fatal(err)
	}
	authManager := &auth.Manager{Server: lifeServer.URL, HTTPClient: lifeServer.Client(), Store: db}

	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request := modelRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch request {
		case 1:
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-unsubscribe-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-unsubscribe","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消课程订阅\"}"}
				}]} ,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		case 2:
			if !bytes.Contains(body, []byte(`\"id\":\"subscription\"`)) ||
				!bytes.Contains(body, []byte(`\"arguments\":[\"remove\",\"CONT5103P.01\"]`)) ||
				bytes.Contains(body, []byte("unsubscribe_section_by_jw_id")) {
				t.Errorf("unsubscribe command documentation=%s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-unsubscribe-call","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-unsubscribe","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}
				}]} ,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		case 3:
			if !bytes.Contains(body, []byte("call-unsubscribe")) || !bytes.Contains(body, []byte("取消订阅")) {
				t.Errorf("literal unsubscribe result missing from resumed request: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-unsubscribe-done","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"已经取消。"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		default:
			t.Errorf("unexpected model request %d", request)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer modelServer.Close()

	handler := commands.Handler{Life: life.NewClient(lifeServer.URL, lifeServer.Client()), Auth: authManager, Store: db}
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model"}, handler, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "取消 COMP6212P.02 的课程订阅", Identity: ident, JobID: job.ID})
	first := svc.Run(ctx, input)
	if first.State != RunStateInterrupted || removeCalls.Load() != 0 {
		t.Fatalf("pre-confirmation result=%#v remove_calls=%d", first, removeCalls.Load())
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionAwaitingConfirmation ||
		executions[0].Effect != string(commands.EffectDestructive) || executions[0].Receipt.Subject != "编译原理（程老师，2026年秋季学期）" {
		t.Fatalf("pending unsubscribe executions=%#v err=%v", executions, err)
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, input.JobLeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause unsubscribe confirmation: ok=%v err=%v", ok, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true}); err != nil || released == nil {
		t.Fatalf("approve unsubscribe: released=%#v err=%v", released, err)
	}
	input = claimAgentInput(t, db, ident, Input{Text: "取消 COMP6212P.02 的课程订阅", Identity: ident, JobID: job.ID})
	final := svc.Run(ctx, input)
	if final.State != RunStateCompleted || final.Response.Text != "已经取消。" || removeCalls.Load() != 1 {
		t.Fatalf("confirmed unsubscribe result=%#v remove_calls=%d", final, removeCalls.Load())
	}
}

func TestMutationExecutionDedupeSurvivesFreshToolCallID(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "dedupe", ConversationType: "private", ConversationID: "dedupe"}
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "semantic-dedupe", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, ok := commands.NewInvocation(commands.CapabilityID("subscription"), []string{"remove", "COMP6212P.02"})
	if !ok {
		t.Fatal("subscription invocation is invalid")
	}
	firstKey := capabilityExecutionDedupeKey(job.ID, "first-model-call", invocation)
	secondKey := capabilityExecutionDedupeKey(job.ID, "fresh-model-call", invocation)
	if firstKey != secondKey {
		t.Fatalf("mutation dedupe changed with tool call ID: %q != %q", firstKey, secondKey)
	}
	first, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, DedupeKey: firstKey, Capability: string(invocation.ID()),
		Arguments: invocation.Args, Effect: string(invocation.Policy().Effect), RequiresConfirmation: true,
	})
	if err != nil || !created {
		t.Fatalf("first execution=%#v created=%v err=%v", first, created, err)
	}
	second, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, DedupeKey: secondKey, Capability: string(invocation.ID()),
		Arguments: invocation.Args, Effect: string(invocation.Policy().Effect), RequiresConfirmation: true,
	})
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("replayed execution=%#v created=%v err=%v first=%#v", second, created, err, first)
	}
	read, _ := commands.NewInvocation(commands.CapabilityCourse, []string{"数学分析"})
	if capabilityExecutionDedupeKey(job.ID, "read-1", read) == capabilityExecutionDedupeKey(job.ID, "read-2", read) {
		t.Fatal("independent read calls were incorrectly collapsed")
	}
}

func TestRunReturnsTerminalMutationReplayWithoutPhantomConfirmation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "terminal-replay", ConversationType: "private", ConversationID: "terminal-replay"}
	handler, _ := destructiveHandlerForTest(t, ctx, db, ident)
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "terminal-replay", Input: store.ConversationJobInput{Text: "退订 COMP6212P.02"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	// A mutation's dedupe key is a content hash of the operation the host will
	// actually prepare, which is the described (resolved) invocation rather than
	// the raw one. Seed through the same preflight so the keys agree.
	raw, ok := commands.NewInvocation(commands.CapabilityID("subscription"), []string{"remove", "COMP6212P.02"})
	if !ok {
		t.Fatal("subscription invocation is invalid")
	}
	described, err := handler.DescribeInvocation(ctx, commands.Input{
		Identity: ident, SuppressLog: true, Origin: commands.InvocationOriginAgent,
	}, raw.ID(), raw.Args)
	if err != nil {
		t.Fatal(err)
	}
	invocation := described.Invocation
	execution, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: input.JobLeaseToken,
		DedupeKey:  capabilityExecutionDedupeKey(job.ID, "old-terminal-call", invocation),
		ToolCallID: "old-terminal-call", Capability: string(invocation.ID()), Arguments: invocation.Args,
		Effect: string(invocation.Policy().Effect),
	})
	if err != nil || !created {
		t.Fatalf("prepare terminal execution=%#v created=%v err=%v", execution, created, err)
	}
	execution, execute, err := db.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, input.JobLeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim terminal execution=%#v execute=%v err=%v", execution, execute, err)
	}
	if _, err := db.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "之前已经完成", nil); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch request {
		case 1:
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-terminal-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-terminal-first","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消订阅课程\"}"}
				}]} ,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-terminal-first","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-terminal-replay","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}
				}]} ,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		case 3:
			if !bytes.Contains(body, []byte("call-terminal-replay")) || !bytes.Contains(body, []byte("之前已经完成")) {
				t.Errorf("terminal replay result missing from model request: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-terminal-done","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"没有重复执行。"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
		default:
			t.Errorf("unexpected model request %d", request)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	second := svc.Run(ctx, input)
	if second.State != RunStateCompleted || second.Response.Text != "没有重复执行。" {
		t.Fatalf("terminal replay run = %#v", second)
	}
	operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 1 || operations[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("terminal replay operations=%#v err=%v", operations, err)
	}
	events, err := db.RecentConversationEvents(ctx, ident, 20)
	if err != nil {
		t.Fatal(err)
	}
	currentResult := false
	for _, event := range events {
		if event.ToolCallID == "call-terminal-replay" && event.Type == store.ConversationEventToolResult &&
			strings.Contains(event.Content, `"result":"之前已经完成"`) {
			// The durable row's own result is replayed, not a fresh execution.
			currentResult = true
		}
	}
	if !currentResult {
		t.Fatalf("terminal replay did not persist the current tool result: %#v", events)
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests=%d want=3", requests.Load())
	}
}

func TestCapabilityExecutionBatchCannotCrossJobOrConversation(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "batch-owner", ConversationType: "private", ConversationID: "batch-owner"}
	prepare := func(sourceEventID, dedupeKey string) store.CapabilityExecution {
		t.Helper()
		job, created, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
			Identity: ident, SourceEventID: sourceEventID, Input: store.ConversationJobInput{Text: sourceEventID}, ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil || !created {
			t.Fatalf("enqueue %s: job=%#v created=%v err=%v", sourceEventID, job, created, err)
		}
		execution, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
			Identity: ident, JobID: job.ID, DedupeKey: dedupeKey, ToolCallID: dedupeKey,
			Capability: string(commands.CapabilityNotify), Arguments: []string{"homework", "on"}, Effect: string(commands.EffectWrite),
		})
		if err != nil || !created {
			t.Fatalf("prepare %s: execution=%#v created=%v err=%v", sourceEventID, execution, created, err)
		}
		return execution
	}
	first := prepare("batch-job-one", "batch-operation-one")
	second := prepare("batch-job-two", "batch-operation-two")

	if _, err := capabilityJobIDForExecutions(ctx, db, []string{first.ID, second.ID}, ident); err == nil ||
		!strings.Contains(err.Error(), "multiple conversation jobs") {
		t.Fatalf("cross-job batch error=%v", err)
	}
	other := ident
	other.ConversationID = "someone-else"
	if _, err := capabilityJobIDForExecutions(ctx, db, []string{first.ID}, other); err == nil ||
		!strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("cross-conversation batch error=%v", err)
	}
}

func TestRunConfirmsParallelMutationsOneOperationAtATime(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "parallel", ConversationType: "private", ConversationID: "parallel"}
	handler, removals := destructiveHandlerForTest(t, ctx, db, ident)
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "parallel-confirm", Input: store.ConversationJobInput{Text: "打开两种通知"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-parallel-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-parallel","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消订阅课程\"}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}
			}`))
			return
		}
		if request == 2 {
			if !bytes.Contains(body, []byte(`\"id\":\"subscription\"`)) {
				t.Errorf("parallel capability request lacks command-search result: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-parallel","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[
					{"id":"call-classes","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"MATH1001.01\"]}"}},
					{"id":"call-homework","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}}
				]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
			}`))
			return
		}
		if request != 3 {
			t.Errorf("unexpected model request %d", request)
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-parallel-done","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"两项都已处理。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}
		}`))
	}))
	defer server.Close()
	var runLog bytes.Buffer
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model", Logger: log.New(&runLog, "", 0)},
		handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "打开两种通知", Identity: ident, JobID: job.ID})
	if first := svc.Run(ctx, input); first.State != RunStateInterrupted {
		t.Fatalf("first run = %#v\nlog:\n%s", first, runLog.String())
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, input.JobLeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause first confirmation job: ok=%v err=%v", ok, err)
	}
	operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 2 {
		t.Fatalf("initial operations=%#v err=%v", operations, err)
	}
	for _, operation := range operations {
		if operation.State != store.CapabilityExecutionAwaitingConfirmation {
			t.Fatalf("operation ran before approval: %#v", operation)
		}
	}

	firstOperation, _, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true})
	if err != nil || firstOperation == nil {
		t.Fatalf("first approval operation=%#v err=%v", firstOperation, err)
	}
	claimed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim first resume=%#v err=%v", claimed, err)
	}
	input = Input{Text: "打开两种通知", Identity: ident, JobID: job.ID, JobRevision: claimed.Revision, JobLeaseToken: claimed.LeaseToken}
	if middle := svc.Run(ctx, input); middle.State != RunStateInterrupted {
		t.Fatalf("middle run = %#v", middle)
	}
	if requests.Load() != 2 {
		t.Fatalf("model ran while a sibling confirmation was pending: %d requests", requests.Load())
	}
	if got := removals.Load(); got != 1 {
		t.Fatalf("exactly the approved sibling should have run: removals=%d", got)
	}
	if ok, err := db.TransitionConversationJob(ctx, claimed.ID, claimed.LeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("return job to confirmation: ok=%v err=%v", ok, err)
	}
	secondOperation, _, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true})
	if err != nil || secondOperation == nil || secondOperation.ID == firstOperation.ID {
		t.Fatalf("second approval operation=%#v err=%v", secondOperation, err)
	}
	input = claimAgentInput(t, db, ident, Input{Text: "打开两种通知", Identity: ident, JobID: job.ID})
	if final := svc.Run(ctx, input); final.State != RunStateCompleted || final.Response.Text != "两项都已处理。" {
		t.Fatalf("final run = %#v", final)
	}
	if got := removals.Load(); got != 2 {
		t.Fatalf("both approved mutations should run: removals=%d", got)
	}
	operations, err = db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 2 {
		t.Fatalf("final operations=%#v err=%v", operations, err)
	}
	for _, operation := range operations {
		if operation.State != store.CapabilityExecutionSucceeded {
			t.Fatalf("operation did not finish: %#v", operation)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestRunFeedsOnlyDeniedConfirmationBackToModel(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "denied", ConversationType: "private", ConversationID: "denied"}
	handler, removals := destructiveHandlerForTest(t, ctx, db, ident)
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "denied-confirm", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var deniedToolResult atomic.Value
	deniedToolResult.Store([]byte(nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-deny-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-deny","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消订阅课程\"}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
			return
		}
		if request == 2 {
			if !bytes.Contains(body, []byte(`\"id\":\"subscription\"`)) {
				t.Errorf("denied capability request lacks command-search result: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-deny","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-deny","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}
				}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
			return
		}
		if request != 3 {
			t.Errorf("unexpected model request %d", request)
		}
		deniedToolResult.Store(append([]byte(nil), body...))
		if !bytes.Contains(body, []byte(`outcome\":\"denied`)) {
			t.Errorf("denial missing from resumed transcript: %s", body)
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-denied-final","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"已成功打开提醒。"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":6,"completion_tokens":2,"total_tokens":8}
		}`))
	}))
	defer server.Close()
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	if first := svc.Run(ctx, input); first.State != RunStateInterrupted {
		t.Fatalf("first run = %#v", first)
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, input.JobLeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause denied confirmation job: ok=%v err=%v", ok, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Reason: "用户拒绝执行"}); err != nil || released == nil {
		t.Fatalf("deny confirmation: released=%#v err=%v", released, err)
	}
	input = claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	if final := svc.Run(ctx, input); final.State != RunStateCompleted {
		t.Fatalf("final run = %#v", final)
	}
	// The host does not rewrite what the model says; it makes sure the model was
	// told the whole outcome. The denial the model receives must state the
	// operation, that nothing ran, and that it must not claim success.
	if !bytes.Contains(deniedToolResult.Load().([]byte), []byte("nothing was changed")) {
		t.Fatalf("denial fed to the model is not self-describing: %s", deniedToolResult.Load())
	}
	if got := removals.Load(); got != 0 {
		t.Fatalf("denied mutation ran: removals=%d", got)
	}
	events, err := db.RecentConversationEvents(ctx, ident, 20)
	if err != nil {
		t.Fatal(err)
	}
	foundDenial := false
	for _, event := range events {
		if event.Type != store.ConversationEventToolDenial {
			continue
		}
		for _, fragment := range []string{`"outcome":"denied"`, "nothing was changed"} {
			if !strings.Contains(event.Content, fragment) {
				t.Fatalf("denial evidence %q lacks %q", event.Content, fragment)
			}
		}
		foundDenial = true
	}
	if !foundDenial {
		t.Fatalf("typed denial event missing: %#v", events)
	}
}

func TestRunRetriesFiveTimesAfterConfirmationResume(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "retry-resume", ConversationType: "private", ConversationID: "retry-resume"}
	handler, _ := destructiveHandlerForTest(t, ctx, db, ident)
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "retry-resume", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-retry-search","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"search-retry-confirm","type":"function","function":{"name":"search_bot_commands","arguments":"{\"query\":\"取消订阅课程\"}"}
				}]} ,"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
			return
		}
		if request == 2 {
			if !bytes.Contains(body, []byte(`\"id\":\"subscription\"`)) {
				t.Errorf("retry capability request lacks command-search result: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-retry-confirm","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-retry-confirm","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"subscription\",\"arguments\":[\"remove\",\"COMP6212P.02\"]}"}
				}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
			}`))
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	}))
	defer server.Close()
	svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"},
		handler, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	if first := svc.Run(ctx, input); first.State != RunStateInterrupted {
		t.Fatalf("first run = %#v", first)
	}
	if ok, err := db.TransitionConversationJob(ctx, job.ID, input.JobLeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause retry confirmation job: ok=%v err=%v", ok, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, store.CapabilityConfirmationDecision{Approved: true}); err != nil || released == nil {
		t.Fatalf("approve confirmation: released=%#v err=%v", released, err)
	}
	input = claimAgentInput(t, db, ident, Input{Text: "退订 COMP6212P.02", Identity: ident, JobID: job.ID})
	second := svc.Run(ctx, input)
	if !second.Handled || !strings.Contains(second.Response.Text, "连续 5 次") {
		t.Fatalf("second run = %#v", second)
	}
	wantRequests := int32(2 + llmHTTPMaxAttempts)
	if requests.Load() != wantRequests {
		t.Fatalf("physical provider requests = %d, want %d", requests.Load(), wantRequests)
	}
	spending, err := db.AgentJobSpending(ctx, job.ID)
	if err != nil || spending.ModelRequests != int64(wantRequests) {
		t.Fatalf("job spending=%#v err=%v", spending, err)
	}
	if _, found, err := db.AgentCheckpoints().Get(ctx, agentCheckpointID(job.ID)); err != nil || !found {
		t.Fatalf("terminal result checkpoint must await acknowledgement: found=%v err=%v", found, err)
	}
	if ok, err := db.CompleteConversationJob(ctx, job.ID, input.JobLeaseToken); err != nil || !ok {
		t.Fatalf("complete retry job: ok=%v err=%v", ok, err)
	}
	if err := svc.Acknowledge(ctx, job.ID, input.JobRevision, input.JobLeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.AgentCheckpoints().Get(ctx, agentCheckpointID(job.ID)); err != nil || found {
		t.Fatalf("acknowledged terminal checkpoint found=%v err=%v", found, err)
	}
}

func TestHandleResponsePropagatesCancellationToModelAndCaller(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "cancel-user", ConversationType: "private", ConversationID: "cancel-user"}
	client := &http.Client{Transport: blockingRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
		return nil, r.Context().Err()
	})}

	svc, err := New(context.Background(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: "http://model.test", Model: "test-model",
	}, commands.Handler{Store: db}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan struct {
		response commands.Response
		ok       bool
	}, 1)
	go func() {
		response, ok := svc.HandleResponse(ctx, Input{
			Text: "等待取消", Identity: ident,
		})
		result <- struct {
			response commands.Response
			ok       bool
		}{response: response, ok: ok}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("model request context was not canceled")
	}
	select {
	case got := <-result:
		if got.ok || got.response.Text != "" || got.response.Image != nil || got.response.Kind != "" || len(got.response.Parts) != 0 {
			t.Fatalf("canceled response = %#v, ok = %v", got.response, got.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled agent run did not return")
	}
	if interrupted, err := db.InterruptStartedAgentRuns(context.Background()); err != nil {
		t.Fatal(err)
	} else if interrupted != 0 {
		t.Fatalf("canceled run remained started: interrupted=%d", interrupted)
	}
}

func TestHandleResponseFinalizesExpiredRunContext(t *testing.T) {
	requestStarted := make(chan struct{})
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "timeout-user", ConversationType: "private", ConversationID: "timeout-user"}
	client := &http.Client{Transport: blockingRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	svc, err := New(context.Background(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: "http://model.test", Model: "test-model",
	}, commands.Handler{Store: db}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentRunCleanupTimeout+100*time.Millisecond)
	defer cancel()
	result := make(chan struct {
		response commands.Response
		ok       bool
	}, 1)
	go func() {
		response, ok := svc.HandleResponse(ctx, Input{Text: "等待超时", Identity: ident})
		result <- struct {
			response commands.Response
			ok       bool
		}{response: response, ok: ok}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	select {
	case got := <-result:
		if !got.ok || !strings.Contains(got.response.Text, "AI 响应超时") {
			t.Fatalf("timed out response = %#v, ok = %v", got.response, got.ok)
		}
	case <-time.After(agentRunCleanupTimeout + 3*time.Second):
		t.Fatal("timed out agent run did not return")
	}
	if interrupted, err := db.InterruptStartedAgentRuns(context.Background()); err != nil {
		t.Fatal(err)
	} else if interrupted != 0 {
		t.Fatalf("timed out run remained started: interrupted=%d", interrupted)
	}
}

type blockingRoundTripper func(*http.Request) (*http.Response, error)

func (f blockingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestToolResultMiddlewarePreservesActualResult(t *testing.T) {
	accumulator := &usageAccumulator{}
	want := strings.Repeat("课", 6010)
	next := func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: want}, nil
	}
	out, err := toolResultMiddleware(nil)(next)(
		withUsageAccumulator(context.Background(), accumulator),
		&compose.ToolInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != want {
		t.Fatalf("tool result was rewritten: got %d runes, want %d", len([]rune(out.Result)), len([]rune(want)))
	}
	if got := accumulator.snapshot().ToolCalls; got != 1 {
		t.Fatalf("tool calls = %d", got)
	}
}

func TestAgentFailureReplyHidesProviderTimeoutAndIncludesTrace(t *testing.T) {
	reply := agentFailureReply(42, context.DeadlineExceeded)
	if !strings.Contains(reply, "AI 响应超时") || !strings.Contains(reply, "记录 #42") {
		t.Fatalf("reply = %q", reply)
	}
	if strings.Contains(reply, "deadline") {
		t.Fatalf("reply exposes provider error: %q", reply)
	}
}

func TestAgentFailureReplyDescribesRunDeadline(t *testing.T) {
	reply := agentFailureReply(308, errAgentRunDeadline)
	if !strings.Contains(reply, "超过 2 分钟") || !strings.Contains(reply, "未能生成完整回复") ||
		!strings.Contains(reply, "可指定数量或筛选条件") || !strings.Contains(reply, "记录 #308") {
		t.Fatalf("reply = %q", reply)
	}
	if strings.Contains(reply, "60 秒") {
		t.Fatalf("reply exposes the obsolete run deadline: %q", reply)
	}
}

func TestImageFailureReplyOnlyExposesInputValidation(t *testing.T) {
	internal := imageFailureReply(7, errors.New("GET https://secret.example: dial tcp 10.0.0.1: refused"))
	if !strings.Contains(internal, "图片处理失败，请稍后重试") || !strings.Contains(internal, "记录 #7") {
		t.Fatalf("internal reply = %q", internal)
	}
	if strings.Contains(internal, "secret.example") || strings.Contains(internal, "10.0.0.1") {
		t.Fatalf("internal details exposed: %q", internal)
	}

	validation := imageFailureReply(8, newImageInputError("图片超过 25 MiB 安全上限"))
	if !strings.Contains(validation, "图片超过 25 MiB 安全上限") || !strings.Contains(validation, "记录 #8") {
		t.Fatalf("validation reply = %q", validation)
	}
}

func TestFinishAgentRunLogsErrors(t *testing.T) {
	var logs bytes.Buffer
	svc := &Service{logger: log.New(&logs, "", 0)}
	svc.finishAgentRun(context.Background(), 0, store.Identity{}, store.AgentRunStatusFailed, "", context.DeadlineExceeded, "deepseek", "test", tokenUsage{}, time.Second)
	if !strings.Contains(logs.String(), "agent run failed") || !strings.Contains(logs.String(), "context deadline exceeded") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestServiceLogsRedactPrivateMCPDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	svc := &Service{logger: log.New(&logs, "", 0)}
	svc.logf("agent tool call failed: %v", errors.New("POST https://private.example/mcp?token=secret-token: connection refused"))
	got := logs.String()
	if strings.Contains(got, "private.example") || strings.Contains(got, "secret-token") {
		t.Fatalf("private MCP diagnostic leaked to logs: %q", got)
	}
	if !strings.Contains(got, "<url>") {
		t.Fatalf("redacted URL marker missing: %q", got)
	}
}

func TestFinishAgentRunLogsUsageAndTotals(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs bytes.Buffer
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc := &Service{logger: log.New(&logs, "", 0), handler: commands.Handler{Store: db}}
	id, err := svc.recordAgentRun(ctx, Input{Text: "hello", Identity: ident}, "kimi", "kimi-k3")
	if err != nil {
		t.Fatal(err)
	}
	svc.finishAgentRun(ctx, id, ident, store.AgentRunStatusCompleted, "ok", nil, "kimi", "kimi-k3", tokenUsage{
		PromptTokens: 100, CachedTokens: 10, CacheMissTokens: 90, CompletionTokens: 20,
		TotalTokens: 120, ModelRequests: 2, ToolCalls: 1,
	}, 1500*time.Millisecond)
	for _, want := range []string{
		"llm run started: id=1 provider=kimi model=kimi-k3",
		"llm run completed: id=1 status=completed provider=kimi model=kimi-k3",
		"prompt_tokens=100 cached_tokens=10 completion_tokens=20 total_tokens=120",
		"model_requests=2 tool_calls=1 estimated_cost_cny=0.003820 duration_ms=1500",
		"llm spending totals: id=1 conversation_cost_cny=0.003820 user_cost_cny=0.003820",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("usage logs missing %q: %q", want, logs.String())
		}
	}
}

func claimAgentInput(t *testing.T, db *store.Store, ident store.Identity, input Input) Input {
	t.Helper()
	claimed, err := db.ClaimConversationJob(t.Context(), ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim agent job: job=%#v err=%v", claimed, err)
	}
	input.JobRevision = claimed.Revision
	input.JobLeaseToken = claimed.LeaseToken
	return input
}
