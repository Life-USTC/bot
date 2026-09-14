package commands

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

func TestResponseModelResultUsesSharedEnvelopeAndStructuredData(t *testing.T) {
	observedAt := time.Date(2026, 9, 14, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	data := map[string]any{
		"operation": "weather",
		"locations": map[string]any{"ustc-main": map[string]any{"temperature": 24.4}},
	}
	response := Response{Text: "天气：\n本部：24° 阴", Data: data}
	encoded := response.ModelResult("weather", "success", observedAt)

	var result toolresult.Result
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatalf("model result JSON = %q: %v", encoded, err)
	}
	if result.Source != "bot" || result.Operation != "weather" || result.Status != "succeeded" {
		t.Fatalf("shared envelope = %#v", result)
	}
	if result.Error != nil {
		t.Fatalf("successful result has error: %#v", result.Error)
	}
	if result.ObservedAt.UTC() != observedAt.UTC() {
		t.Fatalf("observed_at = %s, want %s", result.ObservedAt, observedAt.UTC())
	}
	if resultData, ok := result.Result.(map[string]any); !ok || resultData["operation"] != "weather" {
		t.Fatalf("structured result = %#v", result.Result)
	}
	if strings.Contains(encoded, response.Text) || strings.Contains(encoded, "## ") || strings.Contains(encoded, "| 命令 |") {
		t.Fatalf("presentation leaked into model result: %q", encoded)
	}

	failed := Response{Text: "后端失败的展示文本", Data: data}.ModelResult("weather", "failed", observedAt)
	var failedResult toolresult.Result
	if err := json.Unmarshal([]byte(failed), &failedResult); err != nil {
		t.Fatalf("failed model result JSON = %q: %v", failed, err)
	}
	if failedResult.Error == nil || failedResult.Error.Message != "后端失败的展示文本" {
		t.Fatalf("failed envelope error = %#v", failedResult.Error)
	}
	if failedResult.Result == nil {
		t.Fatal("failed envelope discarded structured data")
	}
}

func TestHelpResponseDataIsStructuredAndSeparateFromRenderedText(t *testing.T) {
	response, ok := (Handler{}).HandleResponse(t.Context(), Input{
		Text:     "帮助",
		Identity: store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"},
	})
	if !ok || response.Text == "" || response.Data == nil {
		t.Fatalf("help response = %#v, handled = %v", response, ok)
	}
	encoded, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	data := string(encoded)
	for _, forbidden := range []string{"Bot 帮助：", "## ", "| 命令 |", "├", "└"} {
		if strings.Contains(data, forbidden) {
			t.Fatalf("help presentation leaked into Data (%q): %s", forbidden, data)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document["type"] != "command_help" {
		t.Fatalf("help Data type = %#v", document["type"])
	}
	if commands, ok := document["commands"].([]any); !ok || len(commands) == 0 {
		t.Fatalf("help Data commands = %#v", document["commands"])
	}
}

func TestHelpOverviewAndManualRespectSharedAudience(t *testing.T) {
	sharedOverview := HelpOverview(true)
	for _, want := range []string{"校园查询：", "天气\t查看本部与高新校区的天气", "课程\t搜索课程和查看课程详情"} {
		if !strings.Contains(sharedOverview, want) {
			t.Fatalf("shared overview missing %q: %s", want, sharedOverview)
		}
	}
	for _, private := range []string{"\n待办（td）\t", "\n课表\t", "\n通知\t", "\n账户\t"} {
		if strings.Contains(sharedOverview, private) {
			t.Fatalf("shared overview exposes private command %q: %s", private, sharedOverview)
		}
	}

	manual := CommandManual(true)
	if !strings.Contains(manual, "weather") || strings.Contains(manual, "todo") || strings.Contains(manual, "schedule") {
		t.Fatalf("shared command manual audience filtering failed: %s", manual)
	}
}
