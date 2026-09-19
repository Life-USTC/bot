package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
)

func boolPointer(value bool) *bool { return &value }

func TestCampusCatalogCacheExpires(t *testing.T) {
	// A fresh lazyMCPSession is created for every turn and every inventory
	// lookup, so the cache is what keeps that from paying a fresh OAuth
	// exchange and tools/list on every request: only the first request after
	// the TTL elapses pays that cost again.
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

func TestCampusCatalogCacheIsNilSafe(t *testing.T) {
	// Service.campusCatalog is always populated by New, but individual tests
	// build a Service literal directly, so a nil cache must behave as an
	// always-miss cache rather than panicking.
	var cache *campusCatalogCache
	if tools, found := cache.get("authenticated"); found || tools != nil {
		t.Fatalf("nil cache get = %#v found=%v", tools, found)
	}
	cache.put("authenticated", []mcpgo.Tool{{Name: "catalog_weather_get"}})
}

func TestCampusCatalogCachePutCopiesTheSlice(t *testing.T) {
	// A cache entry must not alias the caller's slice: appending to the
	// caller's backing array after put must not corrupt what other turns read.
	cache := newCampusCatalogCache()
	source := make([]mcpgo.Tool, 1, 4)
	source[0] = mcpgo.Tool{Name: "catalog_weather_get"}
	cache.put("authenticated", source)
	source[0].Name = "mutated"

	cached, found := cache.get("authenticated")
	if !found || len(cached) != 1 || cached[0].Name != "catalog_weather_get" {
		t.Fatalf("cached entry aliased the caller's slice: %#v", cached)
	}
}

func TestCampusCatalogCacheConcurrentAccessIsRaceFree(t *testing.T) {
	// The cache is shared process-wide across every lazyMCPSession, and the
	// eino tool node runs tool calls in their own goroutines, so concurrent
	// get/put from many turns at once must stay race-free.
	cache := newCampusCatalogCache()
	keys := []string{"authenticated", "anonymous"}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		key := keys[i%len(keys)]
		wg.Add(2)
		go func(key string, index int) {
			defer wg.Done()
			cache.put(key, []mcpgo.Tool{{Name: "catalog_weather_get"}})
		}(key, i)
		go func(key string) {
			defer wg.Done()
			cache.get(key)
		}(key)
	}
	wg.Wait()
}

func TestLazyMCPSessionReusesTheCachedCatalogWithoutRelisting(t *testing.T) {
	// The cache must be consulted by initialize() itself, so every entry point
	// that calls ensure() (search, call, resource/prompt tools, and the
	// capability inventory) benefits, not only one call site.
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
	mcpURL, mcpHTTPClient, closeMCP, _ := newAgentMCPTestServer(t)
	defer closeMCP()

	var listRequests int
	countingClient := &http.Client{Transport: &countingListToolsTransport{
		base: mcpHTTPClient.Transport, count: &listRequests,
	}}
	svc := &Service{
		handler:       commands.Handler{Store: db},
		auth:          &auth.Manager{Store: db},
		mcpClient:     botmcp.New(mcpURL, countingClient),
		campusCatalog: newCampusCatalogCache(),
	}

	first := newLazyMCPSession(svc, ident, 0)
	defer func() { _ = first.Close() }()
	if err := first.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if listRequests != 1 {
		t.Fatalf("tools/list requests after first session = %d, want 1", listRequests)
	}

	second := newLazyMCPSession(svc, ident, 0)
	defer func() { _ = second.Close() }()
	if err := second.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if listRequests != 1 {
		t.Fatalf("tools/list requests after second session = %d, want 1 (cache hit)", listRequests)
	}
	if len(second.tools) != len(first.tools) {
		t.Fatalf("cached session tools = %#v, want same as first = %#v", second.tools, first.tools)
	}
}

// countingListToolsTransport counts tools/list JSON-RPC requests so a test can
// assert the cache actually avoided a network round trip, not merely that it
// returned a plausible-looking result.
type countingListToolsTransport struct {
	base  http.RoundTripper
	count *int
	mu    sync.Mutex
}

func (c *countingListToolsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Body != nil && req.Method == http.MethodPost {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			var rpcRequest struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &rpcRequest) == nil && rpcRequest.Method == string(mcpgo.MethodToolsList) {
				c.mu.Lock()
				*c.count++
				c.mu.Unlock()
			}
		}
	}
	return base.RoundTrip(req)
}

func TestCampusCatalogCacheKeepsAnonymousAndAuthenticatedCatalogsApart(t *testing.T) {
	// The partition follows the token actually obtained, not the shape of the
	// identity. A caller can carry a full Platform/UserID identity and still be
	// logged out, in which case the session opens tokenless and the server
	// returns only the public catalog — that result belongs under "anonymous".
	if got := campusCatalogCacheKey(""); got != "anonymous" {
		t.Fatalf("tokenless key = %q, want anonymous", got)
	}
	if got := campusCatalogCacheKey("mcp-access-token"); got != "authenticated" {
		t.Fatalf("token-bearing key = %q, want authenticated", got)
	}
}

func TestDestructiveCampusCallWaitsForConfirmationWithSharedCatalogCache(t *testing.T) {
	// The server decides whether the user may delete; the host asks whether
	// they want to. That must hold even when the tool catalog came from the
	// shared cache instead of a fresh tools/list.
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
	// Warm the shared cache from a separate session for the same caller first,
	// exactly as inventory.go and toolsFor create independent lazyMCPSession
	// values that must still share one catalog. A real identity is used because
	// every production caller has one: store.Credential rejects an empty
	// identity outright rather than treating it as logged out.
	warm := newLazyMCPSession(svc, ident, 0)
	if err := warm.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_ = warm.Close()

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
	// The host commits the confirmation prompt receipt while it still holds the
	// lease, then parks the job, exactly as the live flow does in
	// mcp_confirmation_flow_test.
	commitAgentConfirmationReceipt(t, db, ctx, ident, job.ID, claimed.LeaseToken,
		"campus-destructive-confirmation-output")
	if _, released, err := db.ResolveCapabilityConfirmation(ctx, ident,
		store.CapabilityConfirmationDecision{
			Reason:        "用户拒绝执行",
			SourceEventID: "campus-destructive-confirmation-1",
		}); err != nil || released == nil {
		t.Fatalf("deny campus operation: released=%#v err=%v", released, err)
	}
	resumed, err := db.ClaimConversationJob(ctx, ident)
	if err != nil || resumed == nil {
		t.Fatalf("reclaim job: %#v err=%v", resumed, err)
	}
	resumeCtx := store.WithConversationJobLease(ctx, job.ID, resumed.LeaseToken)
	result, err := session.resolveCampusExecution(resumeCtx,
		capabilityInterruptState{ExecutionIDs: []string{operations[0].ID}, ToolCallID: operations[0].ToolCallID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls["delete_my_homework"].Load() != 0 {
		t.Fatal("denied campus call still reached the server")
	}
	if !contains(result, `"status": "denied"`) && !contains(result, `"status":"denied"`) {
		t.Fatalf("denied campus result = %q", result)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestLoggedOutCallerCatalogIsCachedSeparatelyFromAuthenticated(t *testing.T) {
	// Having no grant is not a failure and there is nothing to retry: the
	// public half of the catalog needs no token. The cache must still keep
	// that listing apart from any authenticated caller's listing.
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	mcpURL, mcpHTTPClient, closeMCP, _ := newAgentMCPTestServer(t)
	defer closeMCP()

	svc := &Service{
		handler:       commands.Handler{Store: db},
		auth:          &auth.Manager{Store: db},
		mcpClient:     botmcp.New(mcpURL, mcpHTTPClient),
		campusCatalog: newCampusCatalogCache(),
	}
	tools, _, err := svc.toolsFor(ctx, ident, 0, nil)
	if err != nil {
		t.Fatalf("a logged-out caller must still get the public tools: %v", err)
	}
	names := map[string]bool{}
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		names[info.Name] = true
	}
	if !names["search_campus_tools"] || !names["run_bot_command"] {
		t.Fatalf("public tools missing for a logged-out caller: %#v", names)
	}

	// toolsFor only registers the campus tools; lazyMCPSession defers the
	// tools/list round trip until one of them is actually invoked, so nothing
	// is cached yet at this point.
	if _, found := svc.campusCatalog.get("anonymous"); found {
		t.Fatal("registering tools must not have opened a session yet")
	}

	// Invoking a campus tool forces initialize(). The caller has no stored
	// credential, so the session opens tokenless and the server returns the
	// public catalog, which must be filed under the anonymous key.
	session := newLazyMCPSession(svc, ident, 0)
	defer func() { _ = session.Close() }()
	if err := session.ensure(ctx); err != nil {
		t.Fatalf("a logged-out caller must still reach the public catalog: %v", err)
	}
	if _, found := svc.campusCatalog.get("authenticated"); found {
		t.Fatal("a logged-out caller's request must not populate the authenticated cache entry")
	}
	if _, found := svc.campusCatalog.get("anonymous"); !found {
		t.Fatal("a logged-out caller's tools/list result must be cached under the anonymous key")
	}
}

func unusedHTTPTestServerImportGuard() { _ = httptest.NewServer }
