package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func TestModelSelectsAccountInfoImageWithoutRepeatingRead(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var reads atomic.Int32
	api := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/profile" || r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("unexpected profile request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"account-user","name":"Example Student","username":"example-student"}`)
	}))
	t.Cleanup(api.Close)
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(t.Context(), ident, store.Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Resource: api.URL}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var imageID string
	model := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"account-image","type":"function","function":{"name":"run_bot_command","arguments":"{\"command\":\"账户 信息\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, msg := range request.Messages {
			if msg.Role != "tool" {
				continue
			}
			var text string
			if err := json.Unmarshal(msg.Content, &text); err != nil {
				t.Error(err)
				continue
			}
			var result toolresult.Result
			if err := json.Unmarshal([]byte(text), &result); err != nil {
				t.Error(err)
				continue
			}
			if result.Operation != "account" {
				continue
			}
			if result.Status != "succeeded" || len(result.Images) != 1 || !strings.Contains(text, "Example Student") {
				t.Errorf("account tool lost its result or image: %s", text)
				continue
			}
			imageID = result.Images[0].ID
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "![](" + imageID + ")"}, "finish_reason": "stop",
		}}})
	}))
	t.Cleanup(model.Close)
	manager := &auth.Manager{Server: api.URL, HTTPClient: api.Client(), Store: db}
	svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: model.URL, Model: "test"}, commands.Handler{Life: life.NewClient(api.URL, api.Client()), Auth: manager, Store: db}, model.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, handled := svc.HandleResponse(t.Context(), Input{Text: "请显示我的账户信息图卡", Identity: ident, SendResponse: func(context.Context, store.Identity, commands.Response) error {
		t.Error("account image was delivered before model selection")
		return nil
	}})
	if !handled || calls.Load() != 2 || reads.Load() != 1 || imageID == "" || len(response.Parts) != 1 || response.Parts[0].Image == nil {
		t.Fatalf("handled=%v calls=%d reads=%d imageID=%q response=%#v", handled, calls.Load(), reads.Load(), imageID, response)
	}
	image := response.Parts[0].Image
	if !strings.Contains(image.RichText, "已登录：Example Student") || strings.Contains(image.RichText, "账户 帮助") || image.Kind == "help" {
		t.Fatalf("selected image does not match the profile result: %#v", image)
	}
}
