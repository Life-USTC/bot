package commands

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestPublicCommandCacheUsesVersionArgumentsAndTTL(t *testing.T) {
	stateStore, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateStore.Close() }()

	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	cache := NewPublicCommandCache(stateStore, "version-a", 5*time.Minute, nil)
	cache.now = func() time.Time { return now }
	var loads int
	load := func() CapabilityOutcome {
		loads++
		return SuccessOutcome(Response{Text: "课程结果", Data: map[string]any{"courses": []string{"数学分析"}}})
	}

	if got := cache.GetOrLoadOutcome(context.Background(), "course", []string{"数学", "分析"}, load); got.Response.Text != "课程结果" {
		t.Fatalf("first response = %#v", got)
	}
	if got := cache.GetOrLoadOutcome(context.Background(), "course", []string{"数学", "分析"}, load); got.Response.Text != "课程结果" {
		t.Fatalf("cached response = %#v", got)
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}

	cache.GetOrLoadOutcome(context.Background(), "course", []string{"线性代数"}, load)
	if loads != 2 {
		t.Fatalf("loads after different arguments = %d, want 2", loads)
	}

	otherVersion := NewPublicCommandCache(stateStore, "version-b", 5*time.Minute, nil)
	otherVersion.now = cache.now
	otherVersion.GetOrLoadOutcome(context.Background(), "course", []string{"数学", "分析"}, load)
	if loads != 3 {
		t.Fatalf("loads after version change = %d, want 3", loads)
	}

	now = now.Add(5 * time.Minute)
	cache.GetOrLoadOutcome(context.Background(), "course", []string{"数学", "分析"}, load)
	if loads != 4 {
		t.Fatalf("loads after expiration = %d, want 4", loads)
	}
}

func TestPublicCommandCacheDoesNotStoreTemporaryErrors(t *testing.T) {
	stateStore, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateStore.Close() }()

	cache := NewPublicCommandCache(stateStore, "version-a", time.Minute, nil)
	var loads int
	load := func() CapabilityOutcome {
		loads++
		return FailedOutcome(Response{Text: "课程查不到：网络超时，等会儿再试"})
	}
	cache.GetOrLoadOutcome(context.Background(), "course", []string{"数学分析"}, load)
	cache.GetOrLoadOutcome(context.Background(), "course", []string{"数学分析"}, load)
	if loads != 2 {
		t.Fatalf("temporary error loads = %d, want 2", loads)
	}
}

func TestPublicCommandCacheCoalescesConcurrentLoads(t *testing.T) {
	stateStore, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateStore.Close() }()

	cache := NewPublicCommandCache(stateStore, "version-a", time.Minute, nil)
	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	load := func() CapabilityOutcome {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return SuccessOutcome(Response{Text: "学期结果"})
	}

	const callers = 8
	results := make(chan string, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- cache.GetOrLoadOutcome(context.Background(), "semester", nil, load).Response.Text
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)

	for result := range results {
		if result != "学期结果" {
			t.Fatalf("concurrent response = %q", result)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("concurrent loads = %d, want 1", got)
	}
}

func TestPublicCommandCachePurgeRemovesOtherVersions(t *testing.T) {
	stateStore, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateStore.Close() }()

	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	oldCache := NewPublicCommandCache(stateStore, "old", time.Hour, nil)
	oldCache.now = func() time.Time { return now }
	oldCache.GetOrLoadOutcome(context.Background(), "semester", nil, func() CapabilityOutcome { return SuccessOutcome(Response{Text: "旧数据"}) })

	newCache := NewPublicCommandCache(stateStore, "new", time.Hour, nil)
	newCache.now = oldCache.now
	if err := newCache.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := stateStore.PublicCommandCache(context.Background(), "old", "semester", "", now); err != nil || ok {
		t.Fatalf("old cache survived purge: ok = %v, err = %v", ok, err)
	}
}

func TestPublicCommandCacheRetainsDomainDataAcrossInstances(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	first := NewPublicCommandCache(db, "structured", time.Hour, nil)
	first.GetOrLoadOutcome(t.Context(), "weather", []string{"高新"}, func() CapabilityOutcome {
		return SuccessOutcome(Response{Text: "天气卡片", Kind: "weather", Data: map[string]any{"campus": "ustc-gaoxin", "temperature": 27}})
	})
	restarted := NewPublicCommandCache(db, "structured", time.Hour, nil)
	got := restarted.GetOrLoadOutcome(t.Context(), "weather", []string{"高新"}, func() CapabilityOutcome {
		t.Fatal("persistent cache unexpectedly reloaded")
		return FailedOutcome(Response{})
	})
	encoded := got.Response.ModelResult("weather", string(got.Status), time.Now())
	if got.Response.Text != "天气卡片" || !strings.Contains(encoded, `"campus": "ustc-gaoxin"`) || !strings.Contains(encoded, `"temperature": 27`) {
		t.Fatalf("cached result = %s", encoded)
	}
}
