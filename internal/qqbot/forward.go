package qqbot

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/textutil"
)

// QQ exposes referenced/forwarded messages through recursive msg_elements.
// Scene authentication tokens are deliberately absent from messageData and
// cannot become model context. Missing nested timestamps remain unknown.
func qqForwardedElements(elements []messageData, depth int) ([]message.ForwardedMessage, []message.InputMedia) {
	if len(elements) == 0 {
		return nil, nil
	}
	if depth > 8 {
		return []message.ForwardedMessage{{Text: "[引用/转发嵌套层级过深，未继续读取]"}}, nil
	}
	forwards := make([]message.ForwardedMessage, 0, len(elements))
	var media []message.InputMedia
	for _, element := range elements {
		parts, attached := inputPartsFromQQMessage(element.Content, element.Attachments)
		media = append(media, attached...)
		if len(element.ArkData) > 0 && string(element.ArkData) != "null" {
			parts = append(parts, message.InputPart{Type: "text", Text: "分享卡片内容：" + string(element.ArkData)})
		}
		nested, nestedMedia := qqForwardedElements(element.MessageElements, depth+1)
		media = append(media, nestedMedia...)
		for _, child := range nested {
			node := child
			parts = append(parts, message.InputPart{Type: "forward", Forward: &node})
		}
		forwards = append(forwards, message.ForwardedMessage{Speaker: qqForwardActor(element.Author), SentAt: parseQQMessageTime(element.Timestamp), Text: strings.TrimSpace(element.Content), Parts: parts})
	}
	return forwards, media
}

func qqForwardActor(author messageAuthor) message.Actor {
	return message.Actor{Platform: "qqbot", UserID: textutil.FirstNonEmpty(author.UserOpenID, author.MemberOpenID, author.ID), DisplayName: author.Username}
}
