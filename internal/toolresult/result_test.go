package toolresult

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStructuredDataAndObservationSurviveReplay(t *testing.T) {
	at := time.Date(2026, 9, 14, 15, 0, 0, 0, time.FixedZone("CST", 8*3600))
	raw := Encode("mcp", "schedule", "success", at, Data(`{"classes":[{"room":"3A204","start":"15:55"}],"updated_at":"2026-09-13"}`), nil)
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || !result.ObservedAt.Equal(at) || !IsEncoded(raw) {
		t.Fatalf("result=%s", raw)
	}
	data, ok := result.Result.(map[string]any)
	if !ok || data["updated_at"] != "2026-09-13" {
		t.Fatalf("domain data was lost: %#v", result.Result)
	}
	if again := Encode("mcp", "schedule", result.Status, result.ObservedAt, result.Result, nil); again != raw {
		t.Fatalf("replay changed evidence: %s", again)
	}
}

func TestDeniedHasNoResultAndExplicitError(t *testing.T) {
	raw := Encode("bot", "logout", "denied", time.Unix(100, 0), nil, errors.New("用户拒绝，未执行任何变更"))
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result != nil || result.Error == nil || result.Error.Code != "denied" {
		t.Fatalf("result=%s", raw)
	}
}
