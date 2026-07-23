package commands

import (
	"context"
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
	load := func() string {
		loads++
		return "课程结果"
	}

	if got := cache.GetOrLoad(context.Background(), "course", []string{"数学", "分析"}, load); got != "课程结果" {
		t.Fatalf("first response = %q", got)
	}
	if got := cache.GetOrLoad(context.Background(), "course", []string{"数学", "分析"}, load); got != "课程结果" {
		t.Fatalf("cached response = %q", got)
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}

	cache.GetOrLoad(context.Background(), "course", []string{"线性代数"}, load)
	if loads != 2 {
		t.Fatalf("loads after different arguments = %d, want 2", loads)
	}

	otherVersion := NewPublicCommandCache(stateStore, "version-b", 5*time.Minute, nil)
	otherVersion.now = cache.now
	otherVersion.GetOrLoad(context.Background(), "course", []string{"数学", "分析"}, load)
	if loads != 3 {
		t.Fatalf("loads after version change = %d, want 3", loads)
	}

	now = now.Add(5 * time.Minute)
	cache.GetOrLoad(context.Background(), "course", []string{"数学", "分析"}, load)
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
	load := func() string {
		loads++
		return "课程查不到：网络超时，等会儿再试"
	}
	cache.GetOrLoad(context.Background(), "course", []string{"数学分析"}, load)
	cache.GetOrLoad(context.Background(), "course", []string{"数学分析"}, load)
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
	load := func() string {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return "学期结果"
	}

	const callers = 8
	results := make(chan string, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- cache.GetOrLoad(context.Background(), "semester", nil, load)
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
	oldCache.GetOrLoad(context.Background(), "semester", nil, func() string { return "旧数据" })

	newCache := NewPublicCommandCache(stateStore, "new", time.Hour, nil)
	newCache.now = oldCache.now
	if err := newCache.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := stateStore.PublicCommandCache(context.Background(), "old", "semester", "", now); err != nil || ok {
		t.Fatalf("old cache survived purge: ok = %v, err = %v", ok, err)
	}
}
