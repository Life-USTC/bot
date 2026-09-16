package botapp

import (
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"testing"
)

func TestOutboxBundlePreservesImagesTextAndFinalReceipt(t *testing.T) {
	response := commands.Response{Text: "已查询", Parts: []commands.Response{
		{Image: &responses.Image{Kind: "map", URL: "https://example.test/a.png", AltText: "第一张"}},
		{Image: &responses.Image{Kind: "map", URL: "https://example.test/b.png", AltText: "第二张"}},
		{Text: "#校车（成功）\n<search({})>（成功）", Kind: "agent_receipt"},
	}}
	for _, platform := range []string{"napcat", "qqbot"} {
		t.Run(platform, func(t *testing.T) {
			inbound := message.Inbound{Conversation: message.Conversation{Platform: platform, Type: "private", ID: "42"}, Source: message.ReplyRef{MessageID: "source"}}
			outputs, next, err := (&Coordinator{}).responseOutbounds(t.Context(), store.ConversationJob{ID: 8, Revision: 2}, inbound, response, 0)
			if err != nil {
				t.Fatal(err)
			}
			count := 1
			if platform == "qqbot" {
				count = 4
			}
			if len(outputs) != count || next != count {
				t.Fatalf("outputs=%#v next=%d", outputs, next)
			}
			var texts []string
			var intents []string
			var urls []string
			for index, output := range outputs {
				if output.ReplyTo.Sequence != index+1 {
					t.Fatalf("sequence=%d", output.ReplyTo.Sequence)
				}
				if index > 0 && output.DedupeKey == outputs[index-1].DedupeKey {
					t.Fatal("separate sends share dedupe key")
				}
				for _, part := range output.Content.Parts {
					if part.Text != "" {
						texts = append(texts, part.Text)
					}
					if part.Attachment != nil {
						if part.Attachment.URL != "" {
							urls = append(urls, part.Attachment.URL)
						}
						if len(part.Attachment.RenderPayload) > 0 {
							intents = append(intents, part.Attachment.AltText)
						}
					}
				}
			}
			if len(texts) != 0 || len(intents) != 2 || intents[0] != "已查询" || intents[1] != "#校车（成功）\n<search({})>（成功）" || len(urls) != 2 || urls[0] != "https://example.test/a.png" || urls[1] != "https://example.test/b.png" {
				t.Fatalf("texts=%v urls=%v", texts, urls)
			}
		})
	}
}

func TestConfirmationRemainsSeparateFromBundledPresentation(t *testing.T) {
	response := commands.Response{Parts: []commands.Response{{Text: "查询结果", Image: &responses.Image{Kind: "map", URL: "https://example.test/a.png"}}, {Kind: "agent_confirmation", Text: "确认删除第一项？"}, {Kind: "agent_confirmation", Text: "确认删除第二项？"}}}
	outputs, _, err := (&Coordinator{}).responseOutbounds(t.Context(), store.ConversationJob{ID: 9, Revision: 1}, message.Inbound{Conversation: message.Conversation{Platform: "napcat", Type: "private", ID: "42"}}, response, 0)
	if err != nil || len(outputs) != 3 {
		t.Fatalf("outputs=%#v err=%v", outputs, err)
	}
	if outputs[1].Kind != "agent_confirmation" || outputs[1].Content.TextContent() != "确认删除第一项？" || outputs[2].Content.TextContent() != "确认删除第二项？" {
		t.Fatalf("confirmations merged: %#v", outputs)
	}
}
