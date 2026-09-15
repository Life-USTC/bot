package qqbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/message"
)

func TestQQRecursiveMessageElementsPreserveMediaSpeakersAndExcludeSceneTokens(t *testing.T) {
	raw := json.RawMessage(`{"id":"outer","message_type":102,"author":{"user_openid":"owner","username":"Current speaker"},"content":"总结这段记录","timestamp":"2026-09-15T10:00:00Z","message_scene":{"ext":["auth_token=secret-token"]},"msg_elements":[{"author":{"user_openid":"alice","username":"Alice"},"content":"看附件","attachments":[{"url":"https://cdn.example/report.pdf","filename":"report.pdf","content_type":"application/pdf"}],"msg_elements":[{"author":{"id":"bob","username":"Bob"},"content":"嵌套消息","attachments":[{"url":"https://cdn.example/meme.gif","content_type":"image/gif"}]}]}]}`)
	incoming, err := (&Bot{}).messageFromPayload(gatewayPayload{T: "C2C_MESSAGE_CREATE", D: raw})
	if err != nil {
		t.Fatal(err)
	}
	inbound := incoming.inbound()
	if inbound.Actor.UserID != "owner" || inbound.Actor.DisplayName != "Current speaker" || inbound.Text != "总结这段记录" {
		t.Fatalf("outer identity/text changed: %#v", inbound)
	}
	if len(inbound.Forwarded) != 1 || inbound.Forwarded[0].Speaker.UserID != "alice" || inbound.Forwarded[0].Speaker.DisplayName != "Alice" || !inbound.Forwarded[0].SentAt.IsZero() {
		t.Fatalf("forward metadata=%#v", inbound.Forwarded)
	}
	nested := inbound.Forwarded[0].Parts[2].Forward
	if nested == nil || nested.Speaker.UserID != "bob" || nested.Text != "嵌套消息" {
		t.Fatalf("nested=%#v", nested)
	}
	if len(inbound.Media) != 2 || inbound.Media[0].Kind != message.InputMediaFile || len(inbound.ImageURLs) != 1 {
		t.Fatalf("media=%#v images=%#v", inbound.Media, inbound.ImageURLs)
	}
	encoded, _ := json.Marshal(inbound)
	if strings.Contains(string(encoded), "secret-token") {
		t.Fatal("scene token leaked into inbound model material")
	}
}

func TestQQVoiceTranscriptIsPreservedAsPlatformMaterial(t *testing.T) {
	media := inputMediaFromQQAttachments([]map[string]any{{"url": "https://cdn.example/voice.silk", "content_type": "voice", "asr_refer_text": "查询高新校区校车"}})
	if len(media) != 1 || media[0].Kind != message.InputMediaAudio || media[0].Transcript != "查询高新校区校车" {
		t.Fatalf("media=%#v", media)
	}
}
