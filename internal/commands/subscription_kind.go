package commands

import (
	"context"
	"fmt"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

func normalizeSubscriptionKind(value string) string {
	switch normToken(value) {
	case "普通", lifedata.SubscriptionKindRegular:
		return lifedata.SubscriptionKindRegular
	case "助教", lifedata.SubscriptionKindTeachingAssistant:
		return lifedata.SubscriptionKindTeachingAssistant
	case "旁听", lifedata.SubscriptionKindAuditor:
		return lifedata.SubscriptionKindAuditor
	default:
		return ""
	}
}

func subscriptionKindArgsAcceptable(args []string) bool {
	if len(args) != 2 || normalizeSubscriptionKind(args[1]) == "" {
		return false
	}
	_, ok := parseIntArg(args[0])
	return ok
}

func (h Handler) setSubscriptionKind(ctx context.Context, ident store.Identity, args []string) string {
	if !subscriptionKindArgsAcceptable(args) {
		return h.invalidInput("用法：订阅 身份 <JW ID> <普通|助教|旁听>。JW ID 可在订阅列表查看。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	jwID, _ := parseIntArg(args[0])
	kind := normalizeSubscriptionKind(args[1])
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.SetSubscriptionKind(ctx, token, jwID, kind)
	})
	if err != nil {
		return h.commandError("订阅身份更新失败：", err)
	}
	label := lifedata.SubscriptionKindLabel(kind)
	if label == "" {
		label = "普通"
	}
	return fmt.Sprintf("已将教学班 JW ID %d 的订阅身份设为%s。", jwID, label)
}
