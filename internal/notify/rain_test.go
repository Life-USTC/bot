package notify

import (
	"encoding/json"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func rainSnapshot(now time.Time, condition string) map[string]any {
	return map[string]any{"location": map[string]any{"key": "ustc-main", "name": "本部", "adcode": "340100"}, "fetchedAt": now.Format(time.RFC3339), "providers": []any{"amap"}, "current": map[string]any{"source": "amap", "observedAt": now.Format(time.RFC3339), "temperature": float64(16), "condition": map[string]any{"text": condition, "icon": "unknown"}}, "hourly": []any{}, "daily": []any{}, "alerts": []any{}, "extensions": map[string]any{}}
}
func TestRainObservationUsesOnlyFreshActualConditions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name        string
		change      func(map[string]any)
		valid, rain bool
	}{
		{"start", func(s map[string]any) {}, true, true},
		{"stop", func(s map[string]any) { s["current"].(map[string]any)["condition"].(map[string]any)["text"] = "阴" }, true, false},
		{"unknown", func(s map[string]any) { s["current"].(map[string]any)["condition"].(map[string]any)["text"] = "未知" }, false, false},
		{"missing-time", func(s map[string]any) { delete(s["current"].(map[string]any), "observedAt") }, false, false},
		{"stale-observation", func(s map[string]any) {
			s["current"].(map[string]any)["observedAt"] = now.Add(-time.Hour).Format(time.RFC3339)
		}, false, false},
		{"future-observation", func(s map[string]any) {
			s["current"].(map[string]any)["observedAt"] = now.Add(time.Second).Format(time.RFC3339)
		}, false, false},
		{"stale-snapshot", func(s map[string]any) { s["fetchedAt"] = now.Add(-15 * time.Minute).Format(time.RFC3339) }, false, false},
		{"future-snapshot", func(s map[string]any) { s["fetchedAt"] = now.Add(time.Second).Format(time.RFC3339) }, false, false},
		{"modeled", func(s map[string]any) { s["current"].(map[string]any)["source"] = "open-meteo" }, false, false},
		{"wrong-campus", func(s map[string]any) { s["location"].(map[string]any)["key"] = "ustc-gaoxin" }, false, false},
		{"forecast-only", func(s map[string]any) {
			delete(s, "current")
			s["hourly"] = []any{map[string]any{"precipitationProbability": float64(100)}}
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := rainSnapshot(now, "小雨")
			tc.change(s)
			_, rain, valid := rainObservation(s, "ustc-main", now)
			if valid != tc.valid || rain != tc.rain {
				t.Fatalf("valid=%v rain=%v", valid, rain)
			}
		})
	}
}
func TestRainPollerUsesPublicWeatherAndBaselinesRecovery(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/rain.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveRainSettings(t.Context(), store.RainSettings{Identity: ident, Enabled: true, Campus: "ustc-main"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	current := rainSnapshot(now, "阴")
	fail := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/weather" || r.URL.Query().Get("locationKey") != "ustc-main" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		calls++
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	p := &Poller{Store: s, Life: life.NewClient(server.URL, server.Client()), Now: func() time.Time { return now }}
	p.tick(t.Context()) // no OAuth manager or grant is needed
	now = now.Add(time.Minute)
	current = rainSnapshot(now, "小雨")
	p.tick(t.Context())
	records, err := s.ClaimDue(t.Context(), now, 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "开始下雨") {
		t.Fatalf("rain start=%+v err=%v", records, err)
	}
	fail = true
	p.tick(t.Context())
	if allowed, err := s.CanDeliver(t.Context(), records[0].ID); err != nil || allowed {
		t.Fatalf("failed weather left claimed notice active: %v %v", allowed, err)
	}
	fail = false
	now = now.Add(time.Minute)
	current = rainSnapshot(now, "阴")
	p.tick(t.Context())
	records, err = s.ClaimDue(t.Context(), now, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("recovery rain end=%+v %v", records, err)
	}
	if calls != 4 {
		t.Fatalf("weather calls=%d", calls)
	}
}
