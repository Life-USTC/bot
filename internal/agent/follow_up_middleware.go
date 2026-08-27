package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// followUpInbox holds user messages that arrive while an agent run is active.
// They are injected into ChatModelAgent state before the next model call.
type followUpInbox struct {
	mu       sync.Mutex
	messages []Input
}

func newFollowUpInbox() *followUpInbox {
	return &followUpInbox{}
}

func (b *followUpInbox) TryPush(input Input, max int) (ok bool, depth int) {
	if b == nil {
		return false, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if max > 0 && len(b.messages) >= max {
		return false, len(b.messages)
	}
	b.messages = append(b.messages, input)
	return true, len(b.messages)
}

func (b *followUpInbox) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages)
}

func (b *followUpInbox) Push(input Input) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, input)
}

func (b *followUpInbox) Drain() []Input {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.messages) == 0 {
		return nil
	}
	out := append([]Input(nil), b.messages...)
	b.messages = nil
	return out
}

type followUpInjector struct {
	*adk.BaseChatModelAgentMiddleware
	inbox *followUpInbox
}

func newFollowUpInjector(inbox *followUpInbox) adk.ChatModelAgentMiddleware {
	return &followUpInjector{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		inbox:                        inbox,
	}
}

func (i *followUpInjector) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if budget := runBudgetFromContext(ctx); budget != nil {
		if err := budget.contextError(ctx); err != nil {
			return ctx, state, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ctx, state, err
	}
	if i.inbox == nil || state == nil {
		return ctx, state, nil
	}
	for _, followUp := range i.inbox.Drain() {
		if msg := followUpUserMessage(followUp); msg != nil {
			state.Messages = append(state.Messages, msg)
		}
	}
	return ctx, state, nil
}

type stateCapture struct {
	*adk.BaseChatModelAgentMiddleware
	messages []*schema.Message
}

func newStateCapture() *stateCapture {
	return &stateCapture{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

func (c *stateCapture) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if state != nil {
		c.messages = append([]*schema.Message(nil), state.Messages...)
	}
	return ctx, nil
}

func followUpUserMessage(input Input) *schema.Message {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return nil
	}
	return schema.UserMessage(text)
}
