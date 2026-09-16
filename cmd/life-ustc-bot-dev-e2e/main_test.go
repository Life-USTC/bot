package main

import (
	"encoding/base64"
	"encoding/json"
	"github.com/Life-USTC/Bot/internal/responses"
	"os"
	"strings"
	"testing"
)

func TestLoginDetails(t *testing.T) {
	verificationURL, userCode, err := loginDetails(strings.Join([]string{
		"需要登录 Life @ USTC：",
		"http://localhost:3100/oauth/device?code=ABCD-EFGH&step=approve",
		"验证码：ABCD-EFGH",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if verificationURL.Host != "localhost:3100" || verificationURL.Path != "/oauth/device" || userCode != "ABCD-EFGH" {
		t.Fatalf("verificationURL = %s, userCode = %q", verificationURL, userCode)
	}
}

func TestCalendarLink(t *testing.T) {
	calendarURL, err := calendarLink("日历订阅链接：\nhttp://localhost:3100/api/calendar-feeds/user:token.ics\n请勿公开。")
	if err != nil {
		t.Fatal(err)
	}
	if got := calendarURL.String(); got != "http://localhost:3100/api/calendar-feeds/user:token.ics" {
		t.Fatalf("calendar URL = %q", got)
	}
}

func TestPrivateMessageEventPreservesNaturalRequest(t *testing.T) {
	event := privateMessageEvent(testMessage)
	if event["post_type"] != "message" || event["message_type"] != "private" || event["raw_message"] != testMessage {
		t.Fatalf("event = %#v", event)
	}
}

func TestDecodeNapCatMessageSegments(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{
		{"type": "reply", "data": map[string]any{"id": "1001"}},
		{"type": "text", "data": map[string]any{"text": "日历订阅链接：\nhttp://localhost/feed.ics"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeNapCatMessage(raw); got.Text != "日历订阅链接：\nhttp://localhost/feed.ics" || got.ReplyTo != "1001" {
		t.Fatalf("decoded message = %#v", got)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "::1"} {
		if !isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = false", host)
		}
	}
	for _, host := range []string{"example.com", "192.0.2.1", ""} {
		if isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = true", host)
		}
	}
}

func TestEnvironmentWithReplacesInheritedValue(t *testing.T) {
	t.Setenv("LIFE_USTC_SERVER", "https://production.example")
	environment := environmentWith(map[string]string{
		"LIFE_USTC_SERVER": "http://localhost:3100",
	})

	var values []string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "LIFE_USTC_SERVER=") {
			values = append(values, entry)
		}
	}
	if len(values) != 1 || values[0] != "LIFE_USTC_SERVER=http://localhost:3100" {
		t.Fatalf("LIFE_USTC_SERVER entries = %#v", values)
	}
	if _, found := os.LookupEnv("PATH"); found && !containsEnvironmentKey(environment, "PATH") {
		t.Fatal("environmentWith removed unrelated PATH")
	}
}

func containsEnvironmentKey(environment []string, key string) bool {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func TestRenderHarnessMatchesDeliveredImageToLoginContent(t *testing.T) {
	h := newRenderHarness()
	defer h.server.Close()
	r := responses.RemoteRenderer{Endpoint: h.server.URL, Client: h.server.Client()}
	body := "需要登录 Life @ USTC：\nhttp://localhost:3100/oauth/device?code=ABCD-EFGH\n验证码：ABCD-EFGH"
	data, _, _, err := r.RenderPNGContext(t.Context(), responses.NewTextCardImage("login", body))
	if err != nil {
		t.Fatal(err)
	}
	file := "base64://" + base64.StdEncoding.EncodeToString(data)
	raw, err := json.Marshal([]map[string]any{{"type": "image", "data": map[string]any{"file": file}}})
	if err != nil {
		t.Fatal(err)
	}
	message := decodeNapCatMessage(raw)
	if message.Text != "" || len(message.Images) != 1 {
		t.Fatalf("message=%#v", message)
	}
	text, found := h.lookup(message.Images[0])
	if !found {
		t.Fatal("rendered image was not retained")
	}
	uri, code, err := loginDetails(text)
	if err != nil || uri.Host != "localhost:3100" || code != "ABCD-EFGH" {
		t.Fatalf("content=%q err=%v", text, err)
	}
}
