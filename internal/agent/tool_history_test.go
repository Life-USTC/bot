package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestToolHistoryReducerClearsOlderResultsOverBudget(t *testing.T) {
	messages := []*schema.Message{schema.UserMessage("查一下")}
	for i := 0; i < 6; i++ {
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			schema.AssistantMessage("", []schema.ToolCall{{
				ID: callID,
				Function: schema.FunctionCall{
					Name:      "search",
					Arguments: `{"query":"test"}`,
				},
			}}),
			schema.ToolMessage(strings.Repeat("课", maxToolResultRunes), callID),
		)
	}
	reducer := newToolHistoryReducer()
	_, state, err := reducer.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: messages},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleared := 0
	retained := 0
	for _, message := range state.Messages {
		if message.Role != schema.Tool {
			continue
		}
		if message.Content == clearedToolResult {
			cleared++
		} else {
			retained++
		}
	}
	if cleared != 4 || retained != agentToolHistoryRetention {
		t.Fatalf("cleared = %d, retained = %d", cleared, retained)
	}
}

func TestToolHistoryReducerLeavesSmallHistoryUntouched(t *testing.T) {
	message := schema.ToolMessage("short result", "call-1")
	reducer := newToolHistoryReducer()
	_, _, err := reducer.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: []*schema.Message{message}},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "short result" {
		t.Fatalf("message = %#v", message)
	}
}
