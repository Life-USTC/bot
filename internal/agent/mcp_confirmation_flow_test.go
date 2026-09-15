package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestMCPMutationRunsThroughDurableConfirmation(t *testing.T) {
	for _, scenario := range []string{"approve", "deny", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ident := store.Identity{Platform: "napcat", UserID: "mcp-flow", ConversationType: "private", ConversationID: "mcp-flow"}
			if err := db.SaveCredential(ctx, ident, store.Credential{ClientID: "client", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{Identity: ident, SourceEventID: scenario, Input: store.ConversationJobInput{Text: "更新我的测试数据"}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}

			var remoteCalls atomic.Int32
			mcpServer := mcpserver.NewMCPServer("full-mcp", "1.0.0")
			mcpServer.AddTool(mcpgo.NewTool("workspace_future_update", mcpgo.WithReadOnlyHintAnnotation(false), mcpgo.WithDestructiveHintAnnotation(true), mcpgo.WithString("value", mcpgo.Required())),
				func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
					remoteCalls.Add(1)
					if req.GetString("value", "") != "saved-value" {
						t.Errorf("remote arguments = %#v", req.Params.Arguments)
					}
					return mcpgo.NewToolResultText(`{"updated":"saved-value"}`), nil
				})
			mcpHandler := mcpserver.NewStreamableHTTPServer(mcpServer)
			mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				var request struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &request)
				if scenario == "unknown" && request.Method == "tools/call" {
					// The server may have committed the change before losing its response.
					remoteCalls.Add(1)
					http.Error(w, "response lost", http.StatusServiceUnavailable)
					return
				}
				mcpHandler.ServeHTTP(w, r)
			}))
			defer mcpHTTP.Close()
			var modelCalls atomic.Int32
			modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				switch modelCalls.Add(1) {
				case 1:
					_, _ = io.WriteString(w, `{"id":"search","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"mcp-search","type":"function","function":{"name":"search_campus_tools","arguments":"{\"query\":\"*\"}"}}]},"finish_reason":"tool_calls"}]}`)
				case 2:
					if !bytes.Contains(body, []byte("workspace_future_update")) {
						t.Error("server tool was not discovered")
					}
					_, _ = io.WriteString(w, `{"id":"call","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"mcp-mutation","type":"function","function":{"name":"call_campus_tool","arguments":"{\"name\":\"workspace_future_update\",\"arguments\":{\"value\":\"saved-value\"}}"}}]},"finish_reason":"tool_calls"}]}`)
				case 3:
					want := "saved-value"
					if scenario == "deny" {
						want = "用户拒绝"
					}
					if scenario == "unknown" {
						want = "未知"
					}
					if !bytes.Contains(body, []byte("mcp-mutation")) || !bytes.Contains(body, []byte(want)) {
						t.Errorf("resumed transcript missing literal outcome %q", want)
					}
					_, _ = io.WriteString(w, `{"id":"repeat","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"fresh-mcp-mutation","type":"function","function":{"name":"call_campus_tool","arguments":"{\"name\":\"workspace_future_update\",\"arguments\":{\"value\":\"saved-value\"}}"}}]},"finish_reason":"tool_calls"}]}`)
				case 4:
					if !bytes.Contains(body, []byte("fresh-mcp-mutation")) {
						t.Error("repeated mutation result lost the new model call ID")
					}
					_, _ = io.WriteString(w, `{"id":"done","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"处理结束。"},"finish_reason":"stop"}]}`)
				default:
					t.Error("unexpected model replay")
					http.Error(w, "unexpected", http.StatusBadRequest)
				}
			}))
			defer modelHTTP.Close()
			newService := func() *Service {
				manager := &auth.Manager{Store: db}
				svc, err := New(ctx, Config{Enabled: true, APIKey: "test-key", BaseURL: modelHTTP.URL, Model: "test-model", MCPBaseURL: mcpHTTP.URL, AuthManager: manager}, commands.Handler{Store: db, Auth: manager}, mcpHTTP.Client())
				if err != nil {
					t.Fatal(err)
				}
				return svc
			}
			input := claimAgentInput(t, db, ident, Input{Text: "更新我的测试数据", Identity: ident, JobID: job.ID})
			first := newService().Run(ctx, input)
			if first.State != RunStateInterrupted || remoteCalls.Load() != 0 {
				t.Fatalf("before approval: state=%s calls=%d", first.State, remoteCalls.Load())
			}
			commitAgentConfirmationReceipt(t, db, ctx, ident, job.ID, input.JobLeaseToken, scenario+"-confirmation-output")
			decision := store.CapabilityConfirmationDecision{Approved: scenario != "deny"}
			if scenario == "deny" {
				decision.Reason = "用户拒绝执行"
			}
			if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident, decision); err != nil || released == nil {
				t.Fatalf("confirmation: released=%v err=%v", released, err)
			}
			input = claimAgentInput(t, db, ident, Input{Text: "更新我的测试数据", Identity: ident, JobID: job.ID})
			// A fresh service instance must recover the persisted operation and checkpoint.
			final := newService().Run(ctx, input)
			if final.State != RunStateCompleted {
				t.Fatalf("resumed run = %#v", final)
			}
			wantCalls := int32(1)
			wantState := store.CapabilityExecutionSucceeded
			if scenario == "deny" {
				wantCalls, wantState = 0, store.CapabilityExecutionDenied
			}
			if scenario == "unknown" {
				wantState = store.CapabilityExecutionUnknown
			}
			operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(operations) != 2 || operations[0].Capability != "tool:search_campus_tools" || operations[1].State != wantState || remoteCalls.Load() != wantCalls {
				t.Fatalf("operations=%#v calls=%d err=%v", operations, remoteCalls.Load(), err)
			}
			events, err := db.RecentConversationEvents(ctx, ident, 30)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.ToolCallID == "mcp-mutation" && strings.TrimSpace(event.ToolName) == "call_campus_tool" && event.Type != store.ConversationEventAssistant {
					found = true
				}
			}
			if !found {
				t.Fatal("resumed MCP result persisted under the wrong tool name")
			}
			if modelCalls.Load() != 4 {
				t.Fatalf("model calls=%d, want search/call/resume/repeat", modelCalls.Load())
			}
		})
	}
}

func TestMCPInterruptedWriteRecoversOfflineWithoutReplay(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "offline", ConversationType: "private", ConversationID: "offline"}
	now := time.Now()
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{Identity: ident, SourceEventID: "offline", ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimConversationJob(ctx, ident, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	arguments := map[string]any{"value": "saved"}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	callID := campusToolCallID(ctx, job.ID, "workspace_future_update", encoded)
	execution, _, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, LeaseToken: first.LeaseToken, DedupeKey: "offline-write", ToolCallID: callID,
		Capability: "mcp:workspace_future_update", Arguments: []string{string(encoded)}, Effect: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, execute, err := db.ClaimCapabilityExecutionForJob(ctx, execution.ID, job.ID, first.LeaseToken); err != nil || !execute {
		t.Fatalf("claim mutation: execute=%v err=%v", execute, err)
	}
	if ok, err := db.RetryConversationJob(ctx, job.ID, first.LeaseToken, "interrupted process"); err != nil || !ok {
		t.Fatalf("release: ok=%v err=%v", ok, err)
	}
	second, err := db.ClaimConversationJob(ctx, ident, now.Add(time.Second))
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	ctx = store.WithConversationJobLease(ctx, job.ID, second.LeaseToken)
	// No MCP client or OAuth manager: both stale recovery and terminal replay
	// must use the durable record without contacting the server.
	svc := &Service{handler: commands.Handler{Store: db}}
	for i := 0; i < 2; i++ {
		result, err := newLazyMCPSession(svc, ident, job.ID).call(ctx, campusToolCallInput{Name: "workspace_future_update", Arguments: arguments})
		if err != nil || !strings.Contains(result, "未知") {
			t.Fatalf("offline replay %d: result=%q err=%v", i, result, err)
		}
	}
	stored, found, err := db.CapabilityExecution(ctx, execution.ID)
	if err != nil || !found || stored.State != store.CapabilityExecutionUnknown {
		t.Fatalf("stored=%#v found=%v err=%v", stored, found, err)
	}
	_, err = newLazyMCPSession(svc, ident, job.ID+1).resolveCampusExecution(ctx, capabilityInterruptState{ExecutionIDs: []string{execution.ID}, ToolCallID: callID}, false)
	if err == nil || !strings.Contains(err.Error(), "checkpoint belongs to job") {
		t.Fatalf("cross-job checkpoint accepted: %v", err)
	}
}

func TestGraphqlConfirmationFlagComesFromHostApproval(t *testing.T) {
	input := map[string]any{"document": "mutation Update { placeholder }", "confirmed": true}
	pending := campusArgumentsForRemote("graphql_operation_run", input, false)
	approved := campusArgumentsForRemote("graphql_operation_run", pending, true)
	if pending["confirmed"] != false || approved["confirmed"] != true || input["confirmed"] != true {
		t.Fatalf("confirmation flag: input=%#v pending=%#v approved=%#v", input, pending, approved)
	}
}
