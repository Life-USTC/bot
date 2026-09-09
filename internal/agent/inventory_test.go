package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestAsksForCapabilityInventoryRequiresBothCompleteAndCapabilityIntent(t *testing.T) {
	for _, text := range []string{
		"简单列举一下你所有能操控的工具、MCP等",
		"你再确认一下你能调用的所有能力都有哪些",
		"不是啊，为什么一些能力不是 MCP 提供的呢，而是什么平台基础工具，你再确认一下你能调用的所有能力都有哪些，我就是开发，看起来有什么配置出错了",
		"列出完整命令清单",
	} {
		if !asksForCapabilityInventory(text) {
			t.Errorf("asksForCapabilityInventory(%q) = false", text)
		}
	}
	for _, text := range []string{
		"列出前 3 个第二课堂活动",
		"这个工具怎么用",
		"列出工具的使用方法",
		"所有第二课堂活动",
		"关闭所有提醒功能",
		"把全部课程订阅功能取消掉",
		"删除所有待办工具",
		"更新全部设置功能",
		"执行所有可用工具",
	} {
		if asksForCapabilityInventory(text) {
			t.Errorf("asksForCapabilityInventory(%q) = true", text)
		}
	}
}

func TestCapabilityInventoryIsHostGeneratedFromActualRegistries(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "inventory", ConversationType: "private", ConversationID: "inventory"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID: "client", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	job, created, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{
		Identity: ident, SourceEventID: "inventory", Input: store.ConversationJobInput{Text: "简单列举一下你所有能操控的工具、MCP等"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue inventory: created=%v err=%v", created, err)
	}
	mcpURL, mcpHTTPClient, closeMCP, _ := newAgentMCPTestServer(t)
	t.Cleanup(closeMCP)
	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		modelRequests.Add(1)
	}))
	t.Cleanup(modelServer.Close)
	authManager := &auth.Manager{Store: db}
	svc, err := New(t.Context(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model",
		MCPBaseURL: mcpURL, AuthManager: authManager,
	}, commands.Handler{Store: db, Auth: authManager}, mcpHTTPClient)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.Run(t.Context(), claimAgentInput(t, db, ident, Input{
		Text: "简单列举一下你所有能操控的工具、MCP等", Identity: ident, JobID: job.ID,
	}))
	if !result.Handled || result.State != RunStateCompleted {
		t.Fatalf("inventory result = %#v", result)
	}
	response := result.Response
	for _, expected := range []string{"run_bot_command", "weather", "catalog_young_event_list", "catalog_young_event_get"} {
		if !strings.Contains(response.Text, expected) {
			t.Errorf("inventory omitted %q: %s", expected, response.Text)
		}
	}
	// A destructive remote tool is not registered with the model, so listing it
	// as callable would be a lie.
	if strings.Contains(response.Text, "delete_my_homework") {
		t.Fatalf("inventory exposed a withheld destructive tool: %s", response.Text)
	}
	if modelRequests.Load() != 0 {
		t.Fatalf("deterministic inventory made %d model requests", modelRequests.Load())
	}
	events, err := db.RecentConversationEvents(t.Context(), ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != store.ConversationEventUser || events[1].Type != store.ConversationEventAssistant || events[1].Content != response.Text {
		t.Fatalf("inventory events = %#v", events)
	}
}
