package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func asksForCapabilityInventory(text string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	// The shortcut must never preempt a possible operation. Ambiguous wording
	// falls through to the normal model/host policy, where reads and confirmed
	// writes retain their usual authorization semantics.
	for _, marker := range []string{
		"开启", "打开", "关闭", "取消", "删除", "添加", "新增", "更新",
		"修改", "设置", "完成", "恢复", "订阅", "退订", "提交", "执行",
	} {
		if strings.Contains(compact, marker) {
			return false
		}
	}
	wantsAll := false
	for _, marker := range []string{"所有", "全部", "完整", "全量", "一览"} {
		if strings.Contains(compact, marker) {
			wantsAll = true
			break
		}
	}
	if !wantsAll {
		return false
	}
	hasCapabilityNoun := false
	for _, marker := range []string{"命令", "能力", "工具", "mcp", "接口", "功能"} {
		if strings.Contains(compact, marker) {
			hasCapabilityNoun = true
			break
		}
	}
	if !hasCapabilityNoun {
		return false
	}
	for _, marker := range []string{"列举", "列出", "清单", "有哪些", "是什么", "能调用", "可调用", "可用", "支持"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

// capabilityInventory returns the host's real execution surface without
// asking the model to reconstruct it. Bot entries come from the descriptor
// registry; MCP entries are the intersection of the remote tools/list result
// and the host-owned read allowlist.
func (s *Service) capabilityInventory(ctx context.Context, ident store.Identity) (string, error) {
	lines := []string{
		"Agent 当前能力（主机实时配置）：",
		"",
		"一、LLM 直接看到的主机工具",
		"run_bot_command：按用户写法执行一条 Bot 命令（完整命令手册在工具描述里）",
		"get_current_time：查询 Asia/Shanghai 当前时间",
	}
	privateMCP := !store.IsSharedConversation(ident) && s != nil && s.mcpClient != nil && s.auth != nil

	lines = append(lines, "", "二、Bot 能力注册表")
	documentation := commands.SearchCapabilityDocumentation("", commands.CapabilitySearchOptions{
		SharedConversation: store.IsSharedConversation(ident),
	})
	for _, item := range documentation {
		forms := strings.Join(item.Forms, "/")
		lines = append(lines, fmt.Sprintf("%s（%s）：%s", item.ID, forms, item.Summary))
	}

	lines = append(lines, "", "三、当前注册给模型的校园工具（来自服务端目录）")
	switch {
	case store.IsSharedConversation(ident):
		lines = append(lines, "群聊不开放 MCP 工具；请在私聊中检查或使用。")
	case !privateMCP:
		lines = append(lines, "当前 Bot 未配置 MCP 客户端或认证管理器。")
	default:
		session := newLazyMCPSession(s, ident, 0)
		defer func() { _ = session.Close() }()
		if err := session.ensure(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			lines = append(lines, "MCP 当前不可用；Bot 注册表和主机元工具仍如上。")
			break
		}
		names := make([]string, 0, len(session.tools))
		for name, remote := range session.tools {
			if campusEffectOf(remote) == campusEffectDestructive {
				// Withheld from the model, so it must not be listed as callable.
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			lines = append(lines, "服务端当前没有返回可注册的校园工具。")
		}
		for _, name := range names {
			description := strings.TrimSpace(session.tools[name].Description)
			if description == "" {
				lines = append(lines, name)
				continue
			}
			lines = append(lines, name+"："+description)
		}
	}

	lines = append(lines,
		"",
		"结构说明：Bot 命令由主机负责解析、确认、持久化和回执；校园工具直接来自服务端目录，参数以服务端 schema 为准。破坏性校园操作不注册给模型，请改用对应的 Bot 命令。",
	)
	return strings.Join(lines, "\n"), nil
}
