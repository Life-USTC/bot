package agent

import (
	"context"
	"strings"
)

// Preparation reuses the immutable user event for a durable retry. The original
// Inbox envelope stays untouched; extracted file text belongs only to the LLM
// user turn, never to command routing or system instructions.
func (s *Service) prepareInputAttachments(ctx context.Context, input *Input) error {
	if s.handler.Store != nil && input.JobID > 0 {
		event, err := s.handler.Store.ConversationUserEventForJob(ctx, input.Identity, input.JobID)
		if err != nil {
			markAgentInfrastructureFailure(*input, err)
			return err
		}
		if event != nil {
			input.preparedUserEvent = event
			return nil
		}
	}
	input.attachmentContext = FormatForwardedMessages(input.Forwarded)
	if len(input.Media) == 0 {
		return nil
	}
	parser := s.attachmentParser
	if parser == nil {
		var err error
		parser, err = NewAttachmentParser(AttachmentParserConfig{HTTPClient: s.httpClient, Logger: s.logger})
		if err != nil {
			return err
		}
	}
	parsed, err := parser.Parse(ctx, input.Media)
	if err != nil {
		return err
	}
	input.attachmentContext = strings.TrimSpace(input.attachmentContext + "\n\n" + parsed.ContextText())
	for _, imageURL := range parsed.ImageURLs {
		input.ImageURLs = appendUniqueString(input.ImageURLs, imageURL)
	}
	return nil
}
