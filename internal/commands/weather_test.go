package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func weatherTestSnapshot(t *testing.T, locationKey, locationName string, temperature float64) string {
	t.Helper()
	now := time.Now()
	china := lifedata.ChinaLocation()
	hour1 := now.Add(time.Hour).Format(time.RFC3339)
	hour2 := now.Add(2 * time.Hour).Format(time.RFC3339)
	today := now.In(china).Format("2006-01-02")
	tomorrow := now.In(china).Add(24 * time.Hour).Format("2006-01-02")
	return fmt.Sprintf(`{
		"location":{"key":%q,"name":%q,"adcode":"340104"},
		"fetchedAt":%q,
		"providers":["amap","open-meteo"],
		"current":{"temperature":%v,"feelsLike":%v,"humidity":78,"windDirection":"东北","windSpeed":3,"condition":{"text":"阴","icon":"cloudy"}},
		"hourly":[
			{"at":%q,"temperature":%v,"condition":{"text":"晴","icon":"sun"},"precipitationProbability":60},
			{"at":%q,"temperature":%v,"condition":{"text":"多云","icon":"cloud-sun"}}
		],
		"daily":[
			{"date":%q,"temperatureHigh":27,"temperatureLow":20,"condition":{"text":"阴","icon":"cloudy"}},
			{"date":%q,"temperatureHigh":28,"temperatureLow":21,"condition":{"text":"晴","icon":"sun"}}
		],
		"alerts":[]
	}`, locationKey, locationName, now.Format(time.RFC3339),
		temperature, temperature,
		hour1, temperature+1,
		hour2, temperature+2,
		today, tomorrow)
}

func weatherTestHandler(t *testing.T, requested *[]string) Handler {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/weather" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		locationKey := r.URL.Query().Get("locationKey")
		if requested != nil {
			*requested = append(*requested, locationKey)
		}
		switch locationKey {
		case "ustc-main":
			_, _ = w.Write([]byte(weatherTestSnapshot(t, locationKey, "本部", 24.4)))
		case "ustc-gaoxin":
			_, _ = w.Write([]byte(weatherTestSnapshot(t, locationKey, "高新校区", 25.2)))
		default:
			t.Fatalf("unexpected locationKey %q", locationKey)
		}
	}))
	t.Cleanup(server.Close)
	return Handler{Life: life.NewClient(server.URL, server.Client())}
}

func TestWeatherReportsBothCampusesInGroup(t *testing.T) {
	requested := []string{}
	handler := weatherTestHandler(t, &requested)
	input := Input{
		Text: "天气",
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "group",
			ConversationID:   "100",
		},
	}
	reply, ok := handler.Handle(context.Background(), input)
	if !ok {
		t.Fatal("group weather message was not handled")
	}
	plain := textutil.PlainMonospace(reply)
	for _, want := range []string{
		"天气：", "本部：", "高新校区：", "24° 阴", "25° 阴",
		"湿度 78%", "东北风 3 级", "（27° / 20°）",
		"逐小时：", "降水60%", "每日：", "今天 20°~27° 阴",
		"更新于 ", "数据来源：amap、open-meteo",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reply missing %q: %q", want, plain)
		}
	}
	if len(requested) != 2 {
		t.Fatalf("requested locations = %v, want both campuses", requested)
	}
}

func TestWeatherFiltersSingleCampus(t *testing.T) {
	requested := []string{}
	handler := weatherTestHandler(t, &requested)
	reply := handler.weather(context.Background(), []string{"高新"})
	plain := textutil.PlainMonospace(reply)
	if !strings.Contains(plain, "高新校区：") || strings.Contains(plain, "本部：") {
		t.Fatalf("filtered reply = %q", plain)
	}
	if len(requested) != 1 || requested[0] != "ustc-gaoxin" {
		t.Fatalf("requested locations = %v, want only ustc-gaoxin", requested)
	}
}

func TestWeatherRejectsUnknownCampus(t *testing.T) {
	handler := weatherTestHandler(t, nil)
	reply := handler.weather(context.Background(), []string{"火星"})
	if !strings.Contains(reply, "想查哪个校区") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestWeatherImageResponse(t *testing.T) {
	handler := weatherTestHandler(t, nil)
	handler.EnableImageResponses = true
	text := handler.weather(context.Background(), nil)
	image := handler.imageResponseForOutcome(
		Invocation{Name: string(CapabilityWeather)},
		CapabilityOutcome{Status: CapabilityOutcomeSuccess, Response: Response{Text: text}},
	)
	if image == nil {
		t.Fatal("weather image is nil")
	}
	if image.Kind != "weather" || image.Title != "天气" {
		t.Fatalf("image kind/title = %q/%q", image.Kind, image.Title)
	}
	for _, want := range []string{"## 本部", "## 高新校区"} {
		if !strings.Contains(image.RichText, want) {
			t.Fatalf("image rich text missing %q: %q", want, image.RichText)
		}
	}
}
