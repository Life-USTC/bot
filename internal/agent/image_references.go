package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func (s *Service) registerCapabilityImages(ctx context.Context, ident store.Identity, executionID string, invocation commands.Invocation, outcome *commands.CapabilityOutcome) error {
	if outcome.Status != commands.CapabilityOutcomeSuccess || invocation.Policy().Exposure == commands.ExposureHostOnly {
		return nil
	}
	if s.handler.Store == nil {
		if responseContainsImage(outcome.Response) {
			return errors.New("command images require a persistent store")
		}
		return nil
	}
	key := executionID
	if key == "" {
		key = rand.Text()
	}
	return markDurableAgentStateError("save command image references", commands.RegisterResponseImages(ctx, s.handler.Store, ident, "agent:"+key, &outcome.Response))
}

func responseContainsImage(response commands.Response) bool {
	if response.Image != nil {
		return true
	}
	for _, part := range response.Parts {
		if responseContainsImage(part) {
			return true
		}
	}
	return false
}

// This is an attachment reference, never an executable command or remote URL.
var replyImageReference = regexp.MustCompile(`!\[[^\]\r\n]*\]\(([^)\r\n]*)\)`)

func (s *Service) responseWithImageReferences(ctx context.Context, ident store.Identity, reply string) commands.Response {
	matches := replyImageReference.FindAllStringSubmatchIndex(reply, -1)
	if len(matches) == 0 {
		return agentTextResponse(reply)
	}
	response := commands.Response{Kind: "agent"}
	appendText := func(text string) {
		if text = cleanQQReply(text); text != "" {
			response.Parts = append(response.Parts, commands.Response{Text: text, Kind: "agent"})
		}
	}
	offset := 0
	for _, match := range matches {
		appendText(reply[offset:match[0]])
		id := strings.TrimSpace(reply[match[2]:match[3]])
		offset = match[1]
		if s.handler.Store == nil || !strings.HasPrefix(id, "img_") {
			appendText("图片引用无效。")
			continue
		}
		image, found, err := s.handler.Store.CommandImage(ctx, ident, id)
		if err != nil || !found {
			appendText("无法读取这张图片。")
			continue
		}
		response.Parts = append(response.Parts, commands.Response{Image: image, Kind: "agent"})
	}
	appendText(reply[offset:])
	return response
}
