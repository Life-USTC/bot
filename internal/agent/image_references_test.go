package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func TestModelSelectsCommandImageInFinalAnswer(t *testing.T) {
	for _, selectImage := range []bool{true, false} {
		t.Run(fmt.Sprint(selectImage), func(t *testing.T) {
			db, err := store.Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var calls atomic.Int32
			var imageID string
			server := newAgentTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"help-image","type":"function","function":{"name":"run_bot_command","arguments":"{\"command\":\"帮助\"}"}}]},"finish_reason":"tool_calls"}]}`)
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
					if msg.Role == "tool" {
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
						if result.Operation == "help" {
							if len(result.Images) != 1 || result.Result == nil {
								t.Errorf("tool result=%s", text)
								continue
							}
							if !strings.Contains(text, "\n  \"result\":") {
								t.Errorf("not formatted JSON: %s", text)
							}
							imageID = result.Images[0].ID
						}
					}
				}
				answer := "可以从这些命令开始。"
				if selectImage {
					answer = "使用说明\n![](" + imageID + ")\n按需选择。"
				}
				encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}}})
				_, _ = w.Write(encoded)
			}))
			defer server.Close()
			svc, err := New(t.Context(), Config{Enabled: true, APIKey: "test", BaseURL: server.URL, Model: "test"}, commands.Handler{Store: db}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
			response, handled := svc.HandleResponse(t.Context(), Input{Text: "介绍用法", Identity: ident, SendResponse: func(context.Context, store.Identity, commands.Response) error {
				t.Error("command auto-delivered image")
				return nil
			}})
			if !handled || imageID == "" || calls.Load() != 2 {
				t.Fatalf("response=%#v handled=%v image=%s calls=%d", response, handled, imageID, calls.Load())
			}
			if selectImage {
				if len(response.Parts) != 3 || response.Parts[0].Text != "使用说明" || response.Parts[1].Image == nil || response.Parts[2].Text != "按需选择。" {
					t.Fatalf("response=%#v", response)
				}
			} else if len(response.Parts) != 0 || response.Text != "可以从这些命令开始。" {
				t.Fatalf("unselected image leaked: %#v", response)
			}
		})
	}
}

func TestImageReferenceOrderAndScope(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	first, err := db.SaveCommandImage(t.Context(), ident, "first", &responses.Image{Kind: "test", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SaveCommandImage(t.Context(), ident, "second", &responses.Image{Kind: "test", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	reply := svc.responseFor(t.Context(), Input{Identity: ident}, "![]("+second+")\n中间\n![]("+first+")")
	if len(reply.Parts) != 3 || reply.Parts[0].Image.Title != "second" || reply.Parts[1].Text != "中间" || reply.Parts[2].Image.Title != "first" {
		t.Fatalf("order=%#v", reply)
	}
	for _, other := range []store.Identity{{Platform: "napcat", UserID: "43", ConversationType: "private", ConversationID: "42"}, {Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "42"}} {
		response := svc.responseFor(t.Context(), Input{Identity: other}, "![]("+first+")")
		if len(response.Parts) != 1 || response.Parts[0].Image != nil || !strings.Contains(response.Parts[0].Text, "无法读取") {
			t.Fatalf("cross-identity=%#v", response)
		}
	}
	for _, invalid := range []string{"校车", "https://example.test/image.png", "img_missing"} {
		response := svc.responseFor(t.Context(), Input{Identity: ident}, "![]("+invalid+")")
		if len(response.Parts) != 1 || response.Parts[0].Image != nil || strings.Contains(response.Parts[0].Text, invalid) {
			t.Fatalf("invalid=%#v", response)
		}
	}
}
