package responses

import (
	"context"
	"testing"
)

func TestRenderTextAttachmentRequiresInjectedRenderer(t *testing.T) {
	if _, err := RenderTextAttachment(context.Background(), nil, "error", "宿主错误", ""); err == nil {
		t.Fatal("expected missing renderer error")
	}
}

func TestTextCardTitleDoesNotExposeImplementationKind(t *testing.T) {
	image := NewTextCardImage("agent_confirmation", "请确认操作")
	if image == nil || image.Title != "操作确认" || image.Title == image.Kind {
		t.Fatalf("image = %#v", image)
	}
}
