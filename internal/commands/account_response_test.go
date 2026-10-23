package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAccountCommandsRenderTheirOwnResults(t *testing.T) {
	for _, command := range []string{"账户 信息", "我的", "account", "账户", "账户 帮助", "account help"} {
		t.Run(command, func(t *testing.T) {
			var reads atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			ident := testIdentity()
			handler := testAuthedHandler(t, api, ident)
			response, handled := handler.HandleResponse(t.Context(), Input{Text: command, Identity: ident})
			if !handled || response.Image == nil {
				t.Fatalf("handled=%v response=%#v", handled, response)
			}
			assertResponseImageRenders(t, response.Image)
			if command == "账户" || command == "账户 帮助" || command == "account help" {
				if reads.Load() != 0 || response.Image.Kind != "help" || response.Image.Title != "账户 帮助" ||
					!strings.Contains(response.Image.RichText, "账户 信息") {
					t.Fatalf("help read profile or lost its commands: reads=%d image=%#v", reads.Load(), response.Image)
				}
				return
			}
			if reads.Load() != 1 || response.Text != "已登录：Example Student" {
				t.Fatalf("profile reads=%d text=%q", reads.Load(), response.Text)
			}
			if !strings.Contains(response.Image.RichText, "已登录：Example Student") ||
				strings.Contains(response.Image.RichText, "账户 帮助") || response.Image.Kind == "help" {
				t.Fatalf("profile result was replaced by help: %#v", response.Image)
			}
			data, ok := response.Data.(map[string]any)
			if !ok || data["operation"] != "account" {
				t.Fatalf("account data=%#v", response.Data)
			}
			profile, ok := data["profile"].(map[string]any)
			if !ok || profile["name"] != "Example Student" {
				t.Fatalf("profile data=%#v", data["profile"])
			}
		})
	}
}
