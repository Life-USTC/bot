package agent

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

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
	if i == nil || i.inbox == nil || state == nil {
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
	if c != nil && state != nil {
		c.messages = append([]*schema.Message(nil), state.Messages...)
	}
	return ctx, nil
}

func followUpUserMessage(input Input) *schema.Message {
	text := strings.TrimSpace(input.Text)
	if text == "" && len(input.ImageURLs) == 0 && len(input.imageDataURLs) == 0 {
		return nil
	}
	if text == "" {
		text = "（用户补充了一条消息）"
	}
	return schema.UserMessage(text)
}
