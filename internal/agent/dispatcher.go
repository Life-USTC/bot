package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/commands"
)

const (
	defaultDispatchDebounce    = 1200 * time.Millisecond
	defaultDispatchMaxWait     = 3 * time.Second
	defaultDispatchConcurrency = 4
	defaultDispatchMaxPending  = 20
	queueFullReply             = "AI 消息队列已满，请稍后重试。"
)

type DispatcherConfig struct {
	Debounce      time.Duration
	MaxWait       time.Duration
	MaxConcurrent int
	MaxPending    int
	Logger        *log.Logger
}

type DispatchCallback func(context.Context, Input, commands.Response, bool)

type Dispatcher struct {
	ctx        context.Context
	execute    func(context.Context, Input) (commands.Response, bool)
	debounce   time.Duration
	maxWait    time.Duration
	maxPending int
	logger     *log.Logger
	semaphore  chan struct{}

	mu            sync.Mutex
	conversations map[string]*dispatchConversation
}

type dispatchConversation struct {
	pending []dispatchMessage
	wake    chan struct{}
}

type dispatchMessage struct {
	input      Input
	callback   DispatchCallback
	enqueuedAt time.Time
}

func NewDispatcher(ctx context.Context, service *Service, config DispatcherConfig) *Dispatcher {
	execute := func(ctx context.Context, input Input) (commands.Response, bool) {
		if service == nil {
			return commands.Response{}, false
		}
		return service.HandleResponse(ctx, input)
	}
	return newDispatcher(ctx, execute, config)
}

func newDispatcher(ctx context.Context, execute func(context.Context, Input) (commands.Response, bool), config DispatcherConfig) *Dispatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.Debounce <= 0 {
		config.Debounce = defaultDispatchDebounce
	}
	if config.MaxWait <= 0 {
		config.MaxWait = defaultDispatchMaxWait
	}
	if config.MaxWait < config.Debounce {
		config.MaxWait = config.Debounce
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = defaultDispatchConcurrency
	}
	if config.MaxPending <= 0 {
		config.MaxPending = defaultDispatchMaxPending
	}
	return &Dispatcher{
		ctx:           ctx,
		execute:       execute,
		debounce:      config.Debounce,
		maxWait:       config.MaxWait,
		maxPending:    config.MaxPending,
		logger:        config.Logger,
		semaphore:     make(chan struct{}, config.MaxConcurrent),
		conversations: make(map[string]*dispatchConversation),
	}
}

func (d *Dispatcher) Submit(input Input, callback DispatchCallback) {
	key := dispatchConversationKey(input)
	now := time.Now()
	d.mu.Lock()
	conversation := d.conversations[key]
	if conversation == nil {
		conversation = &dispatchConversation{wake: make(chan struct{}, 1)}
		d.conversations[key] = conversation
		go d.runConversation(key, conversation)
	}
	if len(conversation.pending) >= d.maxPending {
		depth := len(conversation.pending)
		d.mu.Unlock()
		d.logf("llm queue rejected: platform=%s conversation_type=%s conversation_id=%s queue_depth=%d reason=full",
			input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, depth)
		if callback != nil {
			callback(d.ctx, input, commands.Response{Text: queueFullReply, Kind: "agent"}, true)
		}
		return
	}
	conversation.pending = append(conversation.pending, dispatchMessage{input: input, callback: callback, enqueuedAt: now})
	depth := len(conversation.pending)
	select {
	case conversation.wake <- struct{}{}:
	default:
	}
	d.mu.Unlock()
	d.logf("llm queue enqueued: platform=%s conversation_type=%s conversation_id=%s queue_depth=%d images=%d",
		input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, depth, len(input.ImageURLs))
}

func (d *Dispatcher) runConversation(key string, conversation *dispatchConversation) {
	for {
		batch, ok := d.nextBatch(key, conversation)
		if !ok {
			return
		}
		select {
		case d.semaphore <- struct{}{}:
		case <-d.ctx.Done():
			d.removeConversation(key, conversation)
			return
		}
		started := time.Now()
		merged := mergeDispatchInputs(batch)
		oldestWait := started.Sub(batch[0].enqueuedAt)
		d.logf("llm batch started: platform=%s conversation_type=%s conversation_id=%s message_count=%d images=%d queue_wait_ms=%d",
			merged.Identity.Platform, merged.Identity.ConversationType, merged.Identity.ConversationID,
			len(batch), len(merged.ImageURLs), oldestWait.Milliseconds())
		response, handled := d.execute(d.ctx, merged)
		<-d.semaphore
		d.logf("llm batch completed: platform=%s conversation_type=%s conversation_id=%s message_count=%d handled=%v duration_ms=%d",
			merged.Identity.Platform, merged.Identity.ConversationType, merged.Identity.ConversationID,
			len(batch), handled, time.Since(started).Milliseconds())
		if callback := batch[len(batch)-1].callback; callback != nil {
			callback(d.ctx, merged, response, handled)
		}
	}
}

func (d *Dispatcher) nextBatch(key string, conversation *dispatchConversation) ([]dispatchMessage, bool) {
	for {
		d.mu.Lock()
		if d.conversations[key] != conversation {
			d.mu.Unlock()
			return nil, false
		}
		if len(conversation.pending) == 0 {
			delete(d.conversations, key)
			d.mu.Unlock()
			return nil, false
		}
		first := conversation.pending[0].enqueuedAt
		last := conversation.pending[len(conversation.pending)-1].enqueuedAt
		now := time.Now()
		idleRemaining := d.debounce - now.Sub(last)
		maxRemaining := d.maxWait - now.Sub(first)
		if idleRemaining <= 0 || maxRemaining <= 0 {
			batch := append([]dispatchMessage(nil), conversation.pending...)
			conversation.pending = nil
			d.mu.Unlock()
			return batch, true
		}
		wait := min(idleRemaining, maxRemaining)
		d.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-d.ctx.Done():
			timer.Stop()
			d.removeConversation(key, conversation)
			return nil, false
		case <-conversation.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func mergeDispatchInputs(batch []dispatchMessage) Input {
	merged := batch[len(batch)-1].input
	texts := make([]string, 0, len(batch))
	images := make([]string, 0)
	for _, message := range batch {
		if text := strings.TrimSpace(message.input.Text); text != "" {
			texts = append(texts, text)
		}
		images = append(images, message.input.ImageURLs...)
	}
	merged.Text = strings.Join(texts, "\n\n")
	merged.ImageURLs = images
	return merged
}

func dispatchConversationKey(input Input) string {
	return fmt.Sprintf("%s\x00%s\x00%s", input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID)
}

func (d *Dispatcher) removeConversation(key string, conversation *dispatchConversation) {
	d.mu.Lock()
	if d.conversations[key] == conversation {
		delete(d.conversations, key)
	}
	d.mu.Unlock()
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d != nil && d.logger != nil {
		d.logger.Printf(format, args...)
	}
}
