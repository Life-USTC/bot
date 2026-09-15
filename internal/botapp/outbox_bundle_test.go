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
				count = 2
			}
			if len(outputs) != count || next != count {
				t.Fatalf("outputs=%#v next=%d", outputs, next)
			}
			var texts []string
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
						urls = append(urls, part.Attachment.URL)
					}
				}
			}
			if len(texts) != 2 || texts[0] != "已查询" || texts[1] != "#校车（成功）\n<search({})>（成功）" || len(urls) != 2 || urls[0] != "https://example.test/a.png" || urls[1] != "https://example.test/b.png" {
				t.Fatalf("texts=%v urls=%v", texts, urls)
			}
		})
	}
}
