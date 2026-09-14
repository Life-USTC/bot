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
	"github.com/Life-USTC/Bot/internal/responses"
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
	response, ok := handler.HandleResponse(context.Background(), input)
	if !ok {
		t.Fatal("group weather message was not handled")
	}
	reply := response.Text
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
	data, ok := response.Data.(map[string]any)
	if !ok || data["operation"] != "weather" {
		t.Fatalf("weather Data = %#v", response.Data)
	}
	locations, ok := data["locations"].(map[string]any)
	if !ok || len(locations) != 2 || locations["ustc-main"] == nil || locations["ustc-gaoxin"] == nil {
		t.Fatalf("weather locations Data = %#v", data["locations"])
	}
}

func TestWeatherCampusAliasesUseOneCanonicalLocation(t *testing.T) {
	for _, alias := range []string{"高新", "高新校区", "高新区", "高新园区", "gaoxin", "gx"} {
		filter, ok := weatherLocationFilter([]string{alias})
		if !ok || filter != "ustc-gaoxin" {
			t.Errorf("weather alias %q = %q, %v; want ustc-gaoxin", alias, filter, ok)
		}
		result := ParseCommand("天气 " + alias)
		if !result.Valid() || result.Invocation.ID() != CapabilityWeather || strings.Join(result.Invocation.Args, " ") != alias {
			t.Errorf("ParseCommand weather alias %q = %#v", alias, result)
		}
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
	outcome := weatherExecutor(handler, context.Background(), store.Identity{}, Invocation{Name: string(CapabilityWeather)})
	if outcome.Status != CapabilityOutcomeSuccess {
		t.Fatalf("outcome status = %v, text = %q", outcome.Status, outcome.Response.Text)
	}
	image := outcome.Response.Image
	if image == nil {
		t.Fatal("weather image is nil")
	}
	if image.Kind != "weather" || image.Title != "天气" {
		t.Fatalf("image kind/title = %q/%q", image.Kind, image.Title)
	}
	if image.Weather == nil {
		t.Fatal("weather card is nil")
	}
	if len(image.Weather.Locations) != 2 {
		t.Fatalf("card locations = %d, want 2", len(image.Weather.Locations))
	}
	for i, want := range []string{"本部", "高新校区"} {
		location := image.Weather.Locations[i]
		if location.Name != want {
			t.Fatalf("location %d name = %q, want %q", i, location.Name, want)
		}
		if len(location.Hourly) == 0 || len(location.Daily) == 0 {
			t.Fatalf("location %q missing hourly/daily points", location.Name)
		}
	}
	if !strings.Contains(image.Weather.Meta, "数据来源：amap") {
		t.Fatalf("card meta = %q", image.Weather.Meta)
	}
	png, width, height, err := (responses.Renderer{}).RenderPNG(image)
	if err != nil {
		t.Fatalf("render weather card: %v", err)
	}
	if len(png) == 0 || width <= 0 || height <= 0 {
		t.Fatalf("rendered png = %d bytes, %dx%d", len(png), width, height)
	}
}
