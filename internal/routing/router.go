package routing

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

type Action string

const (
	ActionIgnore  Action = "ignore"
	ActionCommand Action = "command"
	ActionAgent   Action = "agent"
)

type Activation string

const (
	ActivationDirect      Activation = "direct"
	ActivationCommand     Activation = "command"
	ActivationPublicQuery Activation = "public_query"
	ActivationMention     Activation = "mention"
	ActivationReply       Activation = "reply"
)

type Decision struct {
	Action     Action
	Activation Activation
	Invocation commands.Invocation
}

// Decide is the single pre-persistence routing decision. Ambient shared-chat
// text must leave here as Ignore; execution never performs a second fallback.
func Decide(inbound message.Inbound, replyContext *message.ResponseContext) Decision {
	identity := store.Identity{ConversationType: inbound.Conversation.Type}
	switch store.SurfaceForConversation(identity) {
	case store.ConversationSurfaceDirect:
		return decideDirect(inbound)
	case store.ConversationSurfaceShared:
		return decideShared(inbound, replyContext)
	default:
		return Decision{Action: ActionIgnore}
	}
}

func decideDirect(inbound message.Inbound) Decision {
	parsed := commands.ParseCommand(inbound.Text)
	if parsed.Recognized() {
		return Decision{Action: ActionCommand, Activation: ActivationDirect, Invocation: parsed.Invocation}
	}
	if strings.TrimSpace(inbound.Text) == "" && len(inbound.ImageURLs) == 0 {
		return Decision{Action: ActionIgnore}
	}
	return Decision{Action: ActionAgent, Activation: ActivationDirect}
}

func decideShared(inbound message.Inbound, replyContext *message.ResponseContext) Decision {
	addressed := inbound.BotMentioned || replyContext != nil
	if replyContext != nil {
		if base, ok := commands.NewInvocation(commands.CapabilityID(replyContext.Capability), replyContext.Arguments); ok {
			if invocation, ok := commands.ParsePublicFollowUp(base, inbound.Text); ok {
				return Decision{Action: ActionCommand, Activation: ActivationReply, Invocation: invocation}
			}
		}
	}

	parsed := commands.ParseCommand(inbound.Text)
	if parsed.Recognized() {
		policy := parsed.Invocation.Policy()
		explicit := parsed.Invocation.NaturalRoute == ""
		if policy.DataScope == commands.DataScopePublic {
			activation := ActivationCommand
			if !explicit {
				activation = ActivationPublicQuery
			}
			if inbound.BotMentioned {
				activation = ActivationMention
			} else if replyContext != nil {
				activation = ActivationReply
			}
			return Decision{Action: ActionCommand, Activation: activation, Invocation: parsed.Invocation}
		}
		if policy.DataScope == commands.DataScopeUserPrivate && (explicit || addressed) {
			activation := ActivationCommand
			if inbound.BotMentioned {
				activation = ActivationMention
			} else if replyContext != nil {
				activation = ActivationReply
			}
			return Decision{Action: ActionCommand, Activation: activation, Invocation: parsed.Invocation}
		}
		return Decision{Action: ActionIgnore}
	}

	if addressed {
		activation := ActivationReply
		if inbound.BotMentioned {
			activation = ActivationMention
		}
		return Decision{Action: ActionAgent, Activation: activation}
	}
	return Decision{Action: ActionIgnore}
}
