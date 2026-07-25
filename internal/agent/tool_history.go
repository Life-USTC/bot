package agent

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const clearedToolResult = "[Earlier tool result omitted to keep the conversation within its context budget.]"

type toolHistoryReducer struct {
	*adk.BaseChatModelAgentMiddleware
}

func newToolHistoryReducer() adk.ChatModelAgentMiddleware {
	return &toolHistoryReducer{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

func (r *toolHistoryReducer) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if estimateMessagesTokens(state.Messages) <= int(agentToolHistoryTokenLimit) {
		return ctx, state, nil
	}
	retained := 0
	for i := len(state.Messages) - 1; i >= 0; i-- {
		message := state.Messages[i]
		if message == nil || message.Role != schema.Tool {
			continue
		}
		if retained < agentToolHistoryRetention {
			retained++
			continue
		}
		message.Content = clearedToolResult
		message.MultiContent = nil
		message.UserInputMultiContent = nil
		message.AssistantGenMultiContent = nil
	}
	return ctx, state, nil
}

func estimateMessagesTokens(messages []*schema.Message) int {
	tokens := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		tokens += estimateTextTokens(string(message.Role))
		tokens += estimateTextTokens(message.Content)
		tokens += estimateTextTokens(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			tokens += estimateTextTokens(call.Function.Name)
			tokens += estimateTextTokens(call.Function.Arguments)
		}
		for _, part := range message.UserInputMultiContent {
			tokens += estimateTextTokens(part.Text)
		}
		for _, part := range message.AssistantGenMultiContent {
			tokens += estimateTextTokens(part.Text)
		}
	}
	return tokens
}
