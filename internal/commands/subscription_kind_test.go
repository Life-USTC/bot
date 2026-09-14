package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestSubscriptionKindCommandAndPolicy(t *testing.T) {
	for _, tc := range []struct{ input, kind string }{
		{"订阅 身份 12345 助教", "teaching_assistant"},
		{"订阅 身份 12345 旁听", "auditor"},
		{"subscription kind 12345 regular", "regular"},
	} {
		parsed := ParseCommand(tc.input)
		if parsed.Status != ParseStatusValid || strings.Join(parsed.Invocation.Args, " ") != "kind 12345 "+tc.kind {
			t.Fatalf("parse %q = %#v", tc.input, parsed)
		}
		policy := parsed.Invocation.Policy()
		if policy.Effect != EffectWrite || policy.DataScope != DataScopeUserPrivate {
			t.Fatalf("policy = %#v", policy)
		}
	}
	for _, input := range []string{"订阅 身份 0 助教", "订阅 身份 -1 助教", "订阅 身份 MATH1001.01 助教", "订阅 身份 12345 管理员", "订阅 身份 12345", "订阅 身份 12345 普通 extra"} {
		if parsed := ParseCommand(input); parsed.Status != ParseStatusInvalid {
			t.Fatalf("invalid input %q = %#v", input, parsed)
		}
	}
	group := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "9"}
	outcome, err := (Handler{}).ExecuteCapability(context.Background(), Input{Identity: group}, CapabilitySubscription, []string{"kind", "12345", "auditor"})
	if err != nil || outcome.Status != CapabilityOutcomeForbidden {
		t.Fatalf("group write = %#v, %v", outcome, err)
	}
}

func TestSubscriptionKindUsesExistingMembershipPatch(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		success bool
	}{
		{200, `{"sectionJwId":12345,"kind":"teaching_assistant"}`, true},
		{404, `{"error":"Not subscribed"}`, false},
		{403, `{"error":"Missing subscription scope"}`, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPatch || r.URL.Path != "/api/workspace/subscriptions/12345" || r.Header.Get("Authorization") != "Bearer access" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["kind"] != "teaching_assistant" {
					t.Errorf("body = %#v, err = %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ident := testIdentity()
			h := testAuthedHandler(t, server, ident)
			outcome, handled := h.HandleOutcome(t.Context(), Input{Identity: ident, Text: "订阅 身份 12345 助教"})
			if !handled || calls != 1 || (outcome.Status == CapabilityOutcomeSuccess) != tc.success {
				t.Fatalf("outcome=%#v handled=%v calls=%d", outcome, handled, calls)
			}
			if tc.success && !strings.Contains(outcome.Response.Text, "JW ID 12345 的订阅身份设为助教") {
				t.Fatalf("reply = %q", outcome.Response.Text)
			}
		})
	}
}
