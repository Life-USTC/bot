package agent

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestDispatcherBatchesConsecutiveTextAndImages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputs := make(chan Input, 1)
	dispatcher := newDispatcher(ctx, func(_ context.Context, input Input) (commands.Response, bool) {
		inputs <- input
		return commands.Response{Text: "ok"}, true
	}, DispatcherConfig{Debounce: 20 * time.Millisecond, MaxWait: 60 * time.Millisecond})
	ident := dispatchIdentity("one")
	callbacks := make(chan Input, 2)
	dispatcher.Submit(Input{Text: "先看这张图", ImageURLs: []string{"https://example.test/one.png"}, Identity: ident}, func(_ context.Context, input Input, _ commands.Response, _ bool) {
		callbacks <- input
	})
	time.Sleep(5 * time.Millisecond)
	dispatcher.Submit(Input{Text: "这是我的课表", Identity: ident}, func(_ context.Context, input Input, _ commands.Response, _ bool) {
		callbacks <- input
	})

	select {
	case input := <-inputs:
		if input.Text != "先看这张图\n\n这是我的课表" {
			t.Fatalf("merged text = %q", input.Text)
		}
		if len(input.ImageURLs) != 1 || input.ImageURLs[0] != "https://example.test/one.png" {
			t.Fatalf("merged images = %#v", input.ImageURLs)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for merged input")
	}
	select {
	case input := <-callbacks:
		if input.Text != "先看这张图\n\n这是我的课表" {
			t.Fatalf("callback input = %#v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch callback")
	}
	select {
	case extra := <-callbacks:
		t.Fatalf("unexpected callback for an earlier batched message: %#v", extra)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestDispatcherSerializesConversationAndBatchesWhileActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	inputs := make(chan Input, 2)
	var calls atomic.Int32
	dispatcher := newDispatcher(ctx, func(_ context.Context, input Input) (commands.Response, bool) {
		call := calls.Add(1)
		inputs <- input
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return commands.Response{Text: "ok"}, true
	}, DispatcherConfig{Debounce: 10 * time.Millisecond, MaxWait: 30 * time.Millisecond})
	ident := dispatchIdentity("one")
	dispatcher.Submit(Input{Text: "first", Identity: ident}, nil)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first batch did not start")
	}
	dispatcher.Submit(Input{Text: "second", Identity: ident}, nil)
	dispatcher.Submit(Input{Text: "third", Identity: ident}, nil)
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("same conversation ran concurrently: calls = %d", calls.Load())
	}
	close(releaseFirst)

	first := <-inputs
	second := <-inputs
	if first.Text != "first" || second.Text != "second\n\nthird" {
		t.Fatalf("inputs = %#v, %#v", first, second)
	}
}

func TestDispatcherRunsDifferentConversationsConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	dispatcher := newDispatcher(ctx, func(_ context.Context, input Input) (commands.Response, bool) {
		started <- input.Identity.ConversationID
		<-release
		return commands.Response{Text: "ok"}, true
	}, DispatcherConfig{Debounce: time.Millisecond, MaxWait: 5 * time.Millisecond, MaxConcurrent: 4})
	dispatcher.Submit(Input{Text: "one", Identity: dispatchIdentity("one")}, nil)
	dispatcher.Submit(Input{Text: "two", Identity: dispatchIdentity("two")}, nil)

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case conversationID := <-started:
			seen[conversationID] = true
		case <-time.After(time.Second):
			t.Fatalf("conversations did not run concurrently: %#v", seen)
		}
	}
	close(release)
}

func TestDispatcherRejectsFullConversationQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	dispatcher := newDispatcher(ctx, func(_ context.Context, _ Input) (commands.Response, bool) {
		select {
		case <-firstStarted:
		default:
			close(firstStarted)
		}
		<-release
		return commands.Response{Text: "ok"}, true
	}, DispatcherConfig{
		Debounce:   time.Millisecond,
		MaxWait:    5 * time.Millisecond,
		MaxPending: 2,
	})
	ident := dispatchIdentity("one")
	dispatcher.Submit(Input{Text: "active", Identity: ident}, nil)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("active request did not start")
	}
	dispatcher.Submit(Input{Text: "pending one", Identity: ident}, nil)
	dispatcher.Submit(Input{Text: "pending two", Identity: ident}, nil)
	rejected := make(chan commands.Response, 1)
	dispatcher.Submit(Input{Text: "overflow", Identity: ident}, func(_ context.Context, _ Input, response commands.Response, ok bool) {
		if !ok {
			t.Error("queue rejection was not handled")
		}
		rejected <- response
	})
	select {
	case response := <-rejected:
		if !strings.Contains(response.Text, "队列已满") {
			t.Fatalf("rejection = %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("queue overflow did not respond")
	}
	close(release)
}

func TestDispatcherLogsQueueAndBatchMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := make(chan struct{})
	dispatcher := newDispatcher(ctx, func(_ context.Context, _ Input) (commands.Response, bool) {
		return commands.Response{Text: "ok"}, true
	}, DispatcherConfig{
		Debounce: time.Millisecond,
		MaxWait:  5 * time.Millisecond,
		Logger:   log.New(&logs, "", 0),
	})
	dispatcher.Submit(Input{Text: "hello", Identity: dispatchIdentity("one")}, func(_ context.Context, _ Input, _ commands.Response, _ bool) {
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not finish")
	}
	for _, want := range []string{"llm queue enqueued:", "llm batch started:", "message_count=1", "llm batch completed:"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q: %q", want, logs.String())
		}
	}
}

func dispatchIdentity(conversationID string) store.Identity {
	return store.Identity{
		Platform: "napcat", UserID: conversationID, ConversationType: "private", ConversationID: conversationID,
	}
}
