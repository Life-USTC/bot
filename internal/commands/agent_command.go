package commands

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

const agentConfirmationTTL = 15 * time.Minute

// AgentCommandResult is the host-authoritative result of an Agent-requested
// command. Response is retained for application-layer delivery of images and
// private values; it is deliberately excluded from model-facing JSON.
type AgentCommandResult struct {
	Status               string   `json:"status"`
	Command              string   `json:"command,omitempty"`
	Kind                 string   `json:"kind,omitempty"`
	Text                 string   `json:"text,omitempty"`
	ConfirmationRequired bool     `json:"confirmationRequired,omitempty"`
	DeliveredByHost      bool     `json:"deliveredByHost,omitempty"`
	Response             Response `json:"-"`
}

// ExecuteForAgent parses and executes one host command without feeding it back
// through App.Process. Mutations are converted into a durable confirmation;
// only a real inbound "ok" may execute them through HandleResponse.
func (h Handler) ExecuteForAgent(ctx context.Context, input Input) (AgentCommandResult, error) {
	input.Text = stripCQCodes(input.Text)
	if isConfirmationOK(input.Text) {
		return AgentCommandResult{Status: "forbidden", Text: "Agent 不能代替用户确认操作；请等待用户回复 ok。"}, nil
	}
	cmd, ok := h.parse(input.Text)
	if !ok {
		return AgentCommandResult{Status: "not_found", Text: "宿主无法识别这条命令。"}, nil
	}
	if h.hasAdditionalCommandLine(input.Text) {
		return AgentCommandResult{Status: "forbidden", Text: "一次只能执行一条宿主命令。"}, nil
	}
	if store.IsGroupConversation(input.Identity) && !h.groupCommandAllowed(cmd) {
		return AgentCommandResult{Status: "forbidden", Command: canonicalAgentCommand(cmd), Kind: cmd.Name, Text: "此功能只能在私聊使用。"}, nil
	}
	command := canonicalAgentCommand(cmd)
	if agentCommandNeedsConfirmation(cmd) {
		if h.Store == nil {
			return AgentCommandResult{}, errors.New("confirmation store is unavailable")
		}
		if _, err := h.Store.SavePendingConfirmation(ctx, input.Identity, command, "agent", agentConfirmationTTL); err != nil {
			return AgentCommandResult{}, err
		}
		return AgentCommandResult{
			Status:               "confirmation_required",
			Command:              command,
			Kind:                 cmd.Name,
			Text:                 "需要确认：" + command + "\n回复 ok 后执行。",
			ConfirmationRequired: true,
		}, nil
	}

	response, handled := h.HandleResponse(ctx, Input{
		Text:         command,
		Identity:     input.Identity,
		SuppressLog:  true,
		BotMentioned: input.BotMentioned,
	})
	if !handled {
		return AgentCommandResult{Status: "not_found", Command: command, Kind: cmd.Name, Text: "宿主无法执行这条命令。"}, nil
	}
	return AgentCommandResult{
		Status:          "ok",
		Command:         command,
		Kind:            response.Kind,
		Text:            response.Text,
		DeliveredByHost: agentCommandRequiresHostDelivery(cmd, response),
		Response:        response,
	}, nil
}

func canonicalAgentCommand(cmd parsedCommand) string {
	parts := make([]string, 0, len(cmd.Args)+1)
	parts = append(parts, cmd.Name)
	parts = append(parts, cmd.Args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func agentCommandRequiresHostDelivery(cmd parsedCommand, response Response) bool {
	if response.Image != nil || len(response.Parts) > 0 {
		return true
	}
	return cmd.Name == "subscription" && firstArgIs(cmd.Args, "link")
}

func agentCommandNeedsConfirmation(cmd parsedCommand) bool {
	switch cmd.Name {
	case "logout", "unsubscribe_section_by_jw_id":
		return true
	case "todo":
		if !hasArgs(cmd.Args) || firstArgIs(cmd.Args, "help") {
			return false
		}
		_, _, list, _ := todoListQueryFromArgs(cmd.Args)
		return !list
	case "homework":
		return firstArgIn(cmd.Args, "done", "undo")
	case "subscription":
		return firstArgIs(cmd.Args, "import") || len(extractSectionCodes(joinedArgs(cmd.Args))) > 0
	case "notify":
		return len(cmd.Args) >= 2 && firstArgIn(cmd.Args[1:], "on", "off")
	case "agent":
		return firstArgIn(cmd.Args, "on", "off")
	case "feedback":
		return hasArgs(cmd.Args) && !firstArgIs(cmd.Args, "help")
	case "bus":
		return busPreferenceMutationArgs(cmd.Args)
	default:
		return false
	}
}

func busPreferenceMutationArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	first := normToken(args[0])
	if first == "set" || first == "设置" {
		return true
	}
	if _, ok := parseBusShowDeparted(args); ok {
		return true
	}
	if _, ok := parseBusShowSouth(args); ok {
		return true
	}
	return false
}
