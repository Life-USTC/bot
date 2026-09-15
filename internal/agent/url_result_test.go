package agent

import (
	"context"
	"net/http"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestHandleResponsePreservesModelVisiblePrivateURL(t *testing.T) {
	const want = "订阅链接：https://life.example/api/calendar-feeds/user:token.ics"
	server := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-url","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"` + want + `"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()
	svc, err := New(context.Background(), Config{Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, handled := svc.HandleResponse(context.Background(), Input{
		Text:     "请原样复述测试内容",
		Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
	})
	if !handled || response.Text != want {
		t.Fatalf("response=%#v handled=%v", response, handled)
	}
}
