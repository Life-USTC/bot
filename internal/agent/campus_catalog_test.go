package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
)

func boolPointer(value bool) *bool { return &value }

func TestCampusToolEffectComesFromServerAnnotations(t *testing.T) {
	// The server derives these annotations from the same OAuth scope registry
	// that enforces access, so they are the source of truth rather than a hint.
	read := mcpgo.Tool{Name: "catalog_weather_get"}
	read.Annotations.ReadOnlyHint = boolPointer(true)
	if got := campusEffectOf(read); got != campusEffectRead {
		t.Fatalf("read effect = %q", got)
	}
	write := mcpgo.Tool{Name: "workspace_todo_create"}
	write.Annotations.ReadOnlyHint = boolPointer(false)
	if got := campusEffectOf(write); got != campusEffectWrite {
		t.Fatalf("write effect = %q", got)
	}
	destructive := mcpgo.Tool{Name: "workspace_todo_delete"}
	destructive.Annotations.ReadOnlyHint = boolPointer(false)
	destructive.Annotations.DestructiveHint = boolPointer(true)
	if got := campusEffectOf(destructive); got != campusEffectDestructive {
		t.Fatalf("destructive effect = %q", got)
	}
	// Silence is not a promise of safety.
	if got := campusEffectOf(mcpgo.Tool{Name: "mystery"}); got != campusEffectWrite {
		t.Fatalf("unannotated effect = %q, want write", got)
	}
}

func TestCampusToolKeepsTheServerSchemaVerbatim(t *testing.T) {
	// The enum is the whole point: the Bot's own weather capability accepted
	// 高新 but rejected 高新区 with nothing written down, while the server
	// publishes the exact accepted values. Re-deriving the schema here would
	// lose them again.
	remote := mcpgo.Tool{
		Name:        "catalog_weather_get",
		Description: "Current conditions for one USTC campus location.",
		RawInputSchema: json.RawMessage(`{"type":"object","properties":{"locationKey":{"type":"string",` +
			`"enum":["ustc-main","ustc-gaoxin"],"default":"ustc-main","description":"USTC campus location."}}}`),
	}
	remote.Annotations.ReadOnlyHint = boolPointer(true)

	name, description, rawSchema, err := campusToolInfo(remote)
	if err != nil {
		t.Fatal(err)
	}
	if name != "catalog_weather_get" {
		t.Fatalf("name = %q", name)
	}
	if strings.Contains(description, "changes campus data") {
		t.Fatalf("a read-only tool must not be described as a write: %q", description)
	}
	if !json.Valid(rawSchema) || !strings.Contains(string(rawSchema), `"ustc-gaoxin"`) {
		t.Fatalf("schema lost the server's enum: %s", rawSchema)
	}
}

func TestCampusToolDescriptionMarksWrites(t *testing.T) {
	write := mcpgo.Tool{Name: "workspace_todo_create", Description: "Create a todo."}
	write.Annotations.ReadOnlyHint = boolPointer(false)
	_, description, _, err := campusToolInfo(write)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(description, "changes campus data") {
		t.Fatalf("write description = %q", description)
	}
}

func TestCampusCatalogCacheExpires(t *testing.T) {
	// Registering remote tools natively means listing them before the first
	// model request. The cache is what keeps that from costing an OAuth
	// exchange and a tools/list on every turn.
	now := time.Now()
	cache := newCampusCatalogCache()
	cache.now = func() time.Time { return now }
	cache.put("authenticated", []mcpgo.Tool{{Name: "catalog_weather_get"}})

	if tools, found := cache.get("authenticated"); !found || len(tools) != 1 {
		t.Fatalf("fresh entry = %#v found=%v", tools, found)
	}
	if _, found := cache.get("anonymous"); found {
		t.Fatal("an anonymous caller must not read the authenticated catalog")
	}
	now = now.Add(campusCatalogTTL + time.Second)
	if _, found := cache.get("authenticated"); found {
		t.Fatal("expired entry was served")
	}
}

func TestDestructiveCampusCallWaitsForConfirmation(t *testing.T) {
	// The server decides whether the user may delete; the host asks whether they
	// want to. Same rule as a destructive Bot command, and it must survive the
	// interrupt: nothing may reach the remote service before approval.
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	mcpURL, mcpHTTPClient, closeMCP, calls := newAgentMCPTestServer(t)
	defer closeMCP()
	job, _, err := db.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "campus-destructive", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %#v err=%v", claimed, err)
	}
	svc := &Service{
		handler:       commands.Handler{Store: db},
		auth:          &auth.Manager{Store: db},
		mcpClient:     botmcp.New(mcpURL, mcpHTTPClient),
		campusCatalog: newCampusCatalogCache(),
	}
	session := newLazyMCPSession(svc, ident, job.ID)
	defer func() { _ = session.Close() }()
	callCtx := store.WithConversationJobLease(ctx, job.ID, claimed.LeaseToken)

	if _, err := session.call(callCtx, campusToolCallInput{Name: "delete_my_homework"}); err == nil {
		t.Fatal("destructive call should have interrupted for confirmation")
	}
	if calls["delete_my_homework"].Load() != 0 {
		t.Fatal("destructive call reached the server before approval")
	}
	operations, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(operations) != 1 ||
		operations[0].State != store.CapabilityExecutionAwaitingConfirmation ||
		operations[0].Capability != "mcp:delete_my_homework" ||
		operations[0].Effect != string(commands.EffectDestructive) {
		t.Fatalf("pending campus operation = %#v err=%v", operations, err)
	}

	// Denial keeps the remote service untouched and is reported as the outcome.
	if ok, err := db.TransitionConversationJob(ctx, job.ID, claimed.LeaseToken, store.ConversationJobTransition{
		State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("pause for confirmation: ok=%v err=%v", ok, err)
	}
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident,
		store.CapabilityConfirmationDecision{Reason: "用户拒绝执行"}); err != nil || released == nil {
		t.Fatalf("deny campus operation: released=%#v err=%v", released, err)
	}
	// Resolution re-queues the job, so the resume runs under a fresh lease.
	resumed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || resumed == nil {
		t.Fatalf("reclaim job: %#v err=%v", resumed, err)
	}
	resumeCtx := store.WithConversationJobLease(ctx, job.ID, resumed.LeaseToken)
	result, err := session.resolveCampusExecution(resumeCtx, operations[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls["delete_my_homework"].Load() != 0 {
		t.Fatal("denied campus call still reached the server")
	}
	if !strings.Contains(result, `"outcome":"denied"`) {
		t.Fatalf("denied campus result = %q", result)
	}
}

func TestTransientTokenFailureStillListsPublicTools(t *testing.T) {
	// Production run #331: the OAuth endpoint returned 503 for a moment and a
	// question that needed no token at all — "从东区去西区坐校车要多久" — failed with
	// "校园工具暂时不可用". The public half of the catalog needs no token, so a
	// token failure must never be the reason there are no campus tools.
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	mcpURL, mcpHTTPClient, closeMCP, _ := newAgentMCPTestServer(t)
	defer closeMCP()

	var tokenRequests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer tokenServer.Close()
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(-time.Hour), Resource: tokenServer.URL,
	}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := &Service{
		handler:       commands.Handler{Store: db},
		auth:          &auth.Manager{Server: tokenServer.URL, HTTPClient: tokenServer.Client(), Store: db},
		mcpClient:     botmcp.New(mcpURL, mcpHTTPClient),
		campusCatalog: newCampusCatalogCache(),
		logger:        log.New(&logs, "", 0),
	}
	tools, _, err := svc.toolsFor(ctx, ident, 0, nil)
	if err != nil {
		t.Fatalf("a token outage must not fail the turn: %v", err)
	}
	names := map[string]bool{}
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		names[info.Name] = true
	}
	if !names["search_courses"] || !names["run_bot_command"] {
		t.Fatalf("public campus tools were lost with the token: %#v", names)
	}
	if tokenRequests.Load() < 2 {
		t.Fatalf("token fetch was not retried: %d attempts", tokenRequests.Load())
	}
	if !strings.Contains(logs.String(), "listed anonymously") {
		t.Fatalf("anonymous fallback was not recorded: %q", logs.String())
	}
}
