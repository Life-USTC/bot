package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestMCPAnonymousDiscoveryAndOrdinaryWriteExecutesOnce(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var calls atomic.Int32
	server := mcpserver.NewMCPServer("test", "1")
	server.AddTool(mcpgo.NewTool("public_read", mcpgo.WithReadOnlyHintAnnotation(true), mcpgo.WithString("campus", mcpgo.Description("高新校区 / 高新区"))), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText(`{"campus":"ustc-gaoxin"}`), nil
	})
	server.AddTool(mcpgo.NewTool("ordinary_write", mcpgo.WithReadOnlyHintAnnotation(false), mcpgo.WithDestructiveHintAnnotation(false)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		calls.Add(1)
		return mcpgo.NewToolResultText(`{"saved":true}`), nil
	})
	var authorization atomic.Bool
	handler := mcpserver.NewStreamableHTTPServer(server)
	remote := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authorization.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	ident := store.Identity{Platform: "napcat", UserID: "test", ConversationType: "private", ConversationID: "test"}
	svc := &Service{handler: commands.Handler{Store: db}, auth: &auth.Manager{Store: db}, mcpClient: botmcp.New(remote.URL, remote.Client())}
	anonymous := newLazyMCPSession(svc, ident, 0)
	t.Cleanup(func() { _ = anonymous.Close() })
	result, err := anonymous.search(t.Context(), campusToolSearchInput{Query: "高新区"})
	var found []campusToolDocumentation
	if err != nil || json.Unmarshal([]byte(result), &found) != nil || len(found) != 1 || found[0].Name != "public_read" || authorization.Load() {
		t.Fatalf("anonymous discovery=%s error=%v", result, err)
	}
	result, err = anonymous.call(t.Context(), campusToolCallInput{Name: "public_read"})
	if err != nil || !json.Valid([]byte(result)) {
		t.Fatalf("anonymous read=%s err=%v", result, err)
	}
	job, _, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{Identity: ident, SourceEventID: "write", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimConversationJob(t.Context(), ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%v error=%v", claimed, err)
	}
	ctx := store.WithConversationJobLease(t.Context(), job.ID, claimed.LeaseToken)
	writer := newLazyMCPSession(svc, ident, job.ID)
	t.Cleanup(func() { _ = writer.Close() })
	first, err := writer.call(ctx, campusToolCallInput{Name: "ordinary_write"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := writer.call(ctx, campusToolCallInput{Name: "ordinary_write"})
	if err != nil || first != second || calls.Load() != 1 {
		t.Fatalf("write replay first=%s second=%s calls=%d error=%v", first, second, calls.Load(), err)
	}
	var envelope struct {
		Status string
		Result struct{ Saved bool }
	}
	if json.Unmarshal([]byte(first), &envelope) != nil || envelope.Status != "succeeded" || !envelope.Result.Saved {
		t.Fatalf("write result=%s", first)
	}
	operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 1 || operations[0].ConfirmedAt != nil || operations[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("ordinary write requested confirmation: %#v %v", operations, err)
	}
}
