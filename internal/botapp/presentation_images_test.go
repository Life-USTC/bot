package botapp

import (
	"context"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestPresentationKeepsOnlyExplicitLLMText(t *testing.T) {
	coordinator := &Coordinator{renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
		return []byte("png"), 1, 1, nil
	}), imageRenderTimeout: defaultImageRenderTimeout}
	content, err := coordinator.presentationContentFor(context.Background(), commands.Response{Parts: []commands.Response{
		{Text: "宿主错误", Kind: "agent_error"},
		{Text: "这是模型回答", Kind: "agent", TextOrigin: commands.ResponseTextOriginLLM},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Parts) != 2 || content.Parts[0].Attachment == nil || content.Parts[0].Text != "" || content.Parts[1].Text != "这是模型回答" || content.Parts[1].Attachment != nil {
		t.Fatalf("content = %#v", content)
	}
}

func TestPresentationPrefersSpecializedImageOverSyntheticCaption(t *testing.T) {
	coordinator := &Coordinator{renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
		return []byte("png"), 1, 1, nil
	}), imageRenderTimeout: defaultImageRenderTimeout}
	content, err := coordinator.presentationContentFor(context.Background(), commands.Response{
		Text: "不应重复发送", Kind: "bus", Image: responses.NewTextImage("bus", "校车", "图卡内容"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Parts) != 1 || content.Parts[0].Attachment == nil || content.Parts[0].Text != "" {
		t.Fatalf("content = %#v", content)
	}
}

func TestImageOnlyOutboundPolicyIsExplicitlyMarked(t *testing.T) {
	coordinator := &Coordinator{renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
		return []byte("png"), 1, 1, nil
	})}
	outbounds, _, err := coordinator.responseOutbounds(context.Background(), store.ConversationJob{}, message.Inbound{Conversation: message.Conversation{Platform: "napcat", Type: "private", ID: "42"}}, commands.Response{Text: "宿主提示", Kind: "error"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 1 || outbounds[0].TextPolicy != message.TextPolicyImageOnly || outbounds[0].Content.Parts[0].Text != "" {
		t.Fatalf("outbounds = %#v", outbounds)
	}
}

func TestFlattenResponsePartsPreservesLeadingFields(t *testing.T) {
	image := responses.NewTextImage("bus", "校车", "校车图卡")
	parts := flattenResponseParts(commands.Response{
		Text: "前置说明", Image: image, Kind: "agent",
		Parts: []commands.Response{{Text: "后续回答", TextOrigin: commands.ResponseTextOriginLLM}},
	})
	if len(parts) != 2 || parts[0].Text != "前置说明" || parts[0].Image != image || parts[1].Text != "后续回答" {
		t.Fatalf("parts = %#v", parts)
	}
}
