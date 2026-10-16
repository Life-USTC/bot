package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/specification"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestSpecMCPPerUserDiscovery(t *testing.T) {
	t.Run("bot.mcp-per-user-discovery", func(t *testing.T) {
		contract := specification.Begin(t)
		db, err := store.Open(t.TempDir() + "/bot.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		handlers := map[string]http.Handler{}
		expected := map[string]mcpgo.Tool{}
		for _, user := range []string{"alice", "bob"} {
			server := mcpserver.NewMCPServer(user, "1")
			tool := mcpgo.NewTool("per_user_"+user, mcpgo.WithReadOnlyHintAnnotation(user == "alice"), mcpgo.WithDestructiveHintAnnotation(user == "bob"), mcpgo.WithString("private_"+user, mcpgo.Required()))
			expected[user] = tool
			server.AddTool(tool, func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText(`{"ok":true}`), nil
			})
			handlers["Bearer "+user] = mcpserver.NewStreamableHTTPServer(server)
		}
		remote := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler := handlers[r.Header.Get("Authorization")]
			if handler == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(remote.Close)
		svc := &Service{handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db}, mcpClient: botmcp.New(remote.URL, remote.Client()), campusCatalog: newCampusCatalogCache()}
		for caseIndex, user := range []string{"alice", "bob", "alice"} {
			identity := store.Identity{Platform: "napcat", UserID: user, ConversationType: "private", ConversationID: user}
			if err := db.SaveCredential(t.Context(), identity, store.Credential{ClientID: "client", AccessToken: user, RefreshToken: "refresh-" + user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			session := newLazyMCPSession(svc, identity, 0)
			t.Cleanup(func() { _ = session.Close() })
			raw, err := session.search(t.Context(), campusToolSearchInput{Query: "*"})
			if err != nil {
				t.Fatal(err)
			}
			var docs []campusToolDocumentation
			if err := json.Unmarshal([]byte(raw), &docs); err != nil {
				t.Fatal(err)
			}
			if len(docs) != 1 || docs[0].Name != "per_user_"+user {
				t.Fatalf("%s received another user's catalog: %s", user, raw)
			}
			if !reflect.DeepEqual(docs[0].Annotations, expected[user].Annotations) {
				t.Fatalf("annotations changed: %+v", docs[0].Annotations)
			}
			schema, _ := json.Marshal(expected[user].InputSchema)
			var want, got any
			_ = json.Unmarshal(schema, &want)
			_ = json.Unmarshal(docs[0].InputSchema, &got)
			contract.Check([]string{"alice-first", "bob", "alice-cached"}[caseIndex], specification.Discovery{User: user, Tools: []string{docs[0].Name}, SchemaEqual: reflect.DeepEqual(got, want), AnnotationsEqual: reflect.DeepEqual(docs[0].Annotations, expected[user].Annotations)})
		}
	})
}

func TestSpecSharedConversationSurface(t *testing.T) {
	t.Run("bot.shared-conversation-surface", func(t *testing.T) {
		contract := specification.Begin(t)
		db, err := store.Open(t.TempDir() + "/bot.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		var requests atomic.Int32
		remote := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			http.Error(w, "must not contact MCP", 500)
		}))
		t.Cleanup(remote.Close)
		svc := &Service{handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db}, mcpClient: botmcp.New(remote.URL, remote.Client())}
		for _, kind := range []string{"group", "channel"} {
			identity := store.Identity{Platform: "napcat", UserID: "reader", ConversationType: kind, ConversationID: "shared"}
			tools, session, err := svc.toolsFor(t.Context(), identity, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			if session != nil {
				t.Fatal("shared session exposed MCP")
			}
			names := map[string]bool{}
			for _, tool := range tools {
				info, err := tool.Info(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				names[info.Name] = true
				if strings.Contains(info.Name, "campus") {
					t.Fatalf("private MCP tool exposed: %s", info.Name)
				}
			}
			toolNames := make([]string, 0, len(names))
			for name := range names {
				toolNames = append(toolNames, name)
			}
			sort.Strings(toolNames)
			contract.Check(kind, specification.SharedSurface{Conversation: kind, Tools: toolNames, MCPSession: session != nil, MCPRequests: int(requests.Load())})
		}
		if requests.Load() != 0 {
			t.Fatalf("shared conversations contacted MCP %d times", requests.Load())
		}
	})
}

func TestSpecMCPServerAuthorization(t *testing.T) {
	t.Run("bot.mcp-server-authorization", func(t *testing.T) {
		contract := specification.Begin(t)
		db, err := store.Open(t.TempDir() + "/bot.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		identity := store.Identity{Platform: "napcat", UserID: "restricted", ConversationType: "private", ConversationID: "restricted"}
		if err := db.SaveCredential(t.Context(), identity, store.Credential{ClientID: "client", AccessToken: "restricted", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		bearer := ""
		server := mcpserver.NewMCPServer("authorization", "1")
		server.AddTool(mcpgo.NewTool("future_private_read", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			calls.Add(1)
			return mcpgo.NewToolResultError("permission denied"), nil
		})
		handler := mcpserver.NewStreamableHTTPServer(server)
		remote := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearer = r.Header.Get("Authorization")
			if bearer != "Bearer restricted" {
				t.Error("wrong user's authorization")
			}
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(remote.Close)
		svc := &Service{handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db}, mcpClient: botmcp.New(remote.URL, remote.Client())}
		session := newLazyMCPSession(svc, identity, 0)
		t.Cleanup(func() { _ = session.Close() })
		result, err := session.search(t.Context(), campusToolSearchInput{Query: "*"})
		if err != nil || !strings.Contains(result, "future_private_read") {
			t.Fatalf("dynamic discovery failed: %s %v", result, err)
		}
		result, err = session.call(t.Context(), campusToolCallInput{Name: "future_private_read"})
		contract.Check("denied", specification.Authorization{Bearer: bearer, Calls: int(calls.Load()), Error: err != nil, Result: result})
	})
}

func TestSpecConfirmationBoundToOperation(t *testing.T) {
	t.Run("bot.confirmation-bound-to-operation", func(t *testing.T) {
		contract := specification.Begin(t)
		ctx := t.Context()
		db, err := store.Open(t.TempDir() + "/bot.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		identity := store.Identity{Platform: "napcat", UserID: "confirmed", ConversationType: "private", ConversationID: "confirmed"}
		url, client, closeMCP, calls := newAgentMCPTestServer(t)
		t.Cleanup(closeMCP)
		svc := &Service{handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db}, mcpClient: botmcp.New(url, client)}
		job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{Identity: identity, SourceEventID: "first", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := db.ClaimConversationJob(ctx, identity)
		if err != nil || lease == nil {
			t.Fatalf("claim: %v %v", lease, err)
		}
		pending := newLazyMCPSession(svc, identity, job.ID)
		t.Cleanup(func() { _ = pending.Close() })
		if _, err := pending.call(store.WithConversationJobLease(ctx, job.ID, lease.LeaseToken), campusToolCallInput{Name: "delete_my_homework", Arguments: map[string]any{"id": 1}}); err == nil {
			t.Fatal("expected confirmation")
		}
		executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
		if err != nil || len(executions) != 1 {
			t.Fatalf("pending: %v %v", executions, err)
		}
		commitAgentConfirmationReceipt(t, db, ctx, identity, job.ID, lease.LeaseToken, "operation-confirmation-output")
		if _, _, err := db.ResolveCapabilityConfirmation(ctx, identity, store.CapabilityConfirmationDecision{Approved: true, SourceEventID: "operation-confirmed"}); err != nil {
			t.Fatal(err)
		}
		resumed, err := db.ClaimConversationJob(ctx, identity)
		if err != nil || resumed == nil {
			t.Fatalf("resume: %v %v", resumed, err)
		}
		resumedContext := store.WithConversationJobLease(ctx, job.ID, resumed.LeaseToken)
		session := newLazyMCPSession(svc, identity, job.ID)
		t.Cleanup(func() { _ = session.Close() })
		if _, err := session.resolveCampusExecution(resumedContext, capabilityInterruptState{ExecutionIDs: []string{executions[0].ID}, ToolCallID: executions[0].ToolCallID}, true); err != nil {
			t.Fatal(err)
		}
		if calls["delete_my_homework"].Load() != 1 {
			t.Fatal("approved operation did not execute once")
		}
		if _, err := session.call(resumedContext, campusToolCallInput{Name: "delete_my_homework", Arguments: map[string]any{"id": 2}}); err == nil {
			t.Fatal("approval was reused for a different target")
		}
		if calls["delete_my_homework"].Load() != 1 {
			t.Fatal("different target reached server without approval")
		}
		executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
		if err != nil || len(executions) != 2 {
			t.Fatalf("executions: %v %v", executions, err)
		}
		contract.Check("different-target", specification.Confirmation{State: string(executions[1].State), RemoteCalls: int(calls["delete_my_homework"].Load()), DecisionRecorded: executions[1].ConfirmedAt != nil})
	})
}
