package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteRendererWeatherPayload(t *testing.T) {
	var got remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(testRemotePNG(t))
	}))
	defer server.Close()

	card := &WeatherCard{
		Meta: "更新于 15:04 · 数据来源：和风天气",
		Locations: []WeatherCardLocation{{
			Name: "本部",
			Current: WeatherCardCurrent{
				Temperature:   24.4,
				ConditionText: "阴",
				Icon:          "cloudy",
				High:          27,
				Low:           20,
				HasRange:      true,
				HumidityText:  "78%",
				WindText:      "东北风 3 级",
			},
			Hourly: []WeatherCardHourPoint{{Label: "16:00", Temperature: 25, PrecipitationProbability: 60}},
			Daily:  []WeatherCardDayPoint{{Label: "今天", Low: 20, High: 27, ConditionText: "阴"}},
			Alerts: []string{"高温黄色预警"},
		}},
	}
	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return time.Date(2026, 9, 2, 15, 4, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}
	img := NewWeatherCardImage(card, "本部：24° 阴")
	if img == nil {
		t.Fatal("weather image is nil")
	}
	// Weather parity uses the fixed legacy title even for a manually assembled
	// image whose title does not come from NewWeatherCardImage.
	img.Title = "自定义标题"
	_, width, height, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	if width != 8 || height != 6 {
		t.Fatalf("dimensions = %dx%d, want 8x6", width, height)
	}
	if got.Kind != "weather" {
		t.Fatalf("request kind = %q, want weather", got.Kind)
	}
	var payload remoteWeatherPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("decode weather payload: %v", err)
	}
	if payload.Title != "天气" || payload.Meta != card.Meta {
		t.Fatalf("title/meta = %q/%q", payload.Title, payload.Meta)
	}
	if payload.CanvasWidth != remoteWeatherCanvasWidth || payload.Height != remoteWeatherLogicalHeight(card) {
		t.Fatalf("geometry = %dx%d, want %dx%d", payload.CanvasWidth, payload.Height, remoteWeatherCanvasWidth, remoteWeatherLogicalHeight(card))
	}
	if len(payload.Footer) != 2 || payload.Footer[0] != "15:04 · 工作日" || payload.Footer[1] != "Life @ USTC" {
		t.Fatalf("footer = %#v", payload.Footer)
	}
	if len(payload.Locations) != 1 {
		t.Fatalf("locations = %#v", payload.Locations)
	}
	location := payload.Locations[0]
	if len(location.Hourly) != 1 || location.Hourly[0].PrecipitationProbability != 60 {
		t.Fatalf("hourly = %#v", location.Hourly)
	}
	if len(location.Daily) != 1 || location.Daily[0].High != 27 {
		t.Fatalf("daily = %#v", location.Daily)
	}
}

func TestRemoteRendererRejectsEmptyWeatherCard(t *testing.T) {
	renderer := RemoteRenderer{Endpoint: "http://127.0.0.1:1/render"}
	img := &Image{Kind: "weather", AltText: "天气", Weather: &WeatherCard{}}
	_, _, _, err := renderer.RenderPNG(img)
	if err == nil || !strings.Contains(err.Error(), "no locations") {
		t.Fatalf("err = %v, want no-locations validation error", err)
	}
}

func TestRemoteWeatherLogicalHeightMatchesLegacyRows(t *testing.T) {
	card := &WeatherCard{Locations: []WeatherCardLocation{{Name: "空"}}}
	if got, want := remoteWeatherLogicalHeight(card), 64+34+110+30+48; got != want {
		t.Fatalf("height = %d, want %d", got, want)
	}
	card.Locations[0].Current.HumidityText = "50%"
	card.Locations[0].Hourly = []WeatherCardHourPoint{{Label: "now", Temperature: 20}}
	card.Locations[0].Daily = []WeatherCardDayPoint{{Label: "今天", Low: 18, High: 22}}
	if got, want := remoteWeatherLogicalHeight(card), 64+34+110+68+(32+158+20)+(32+30)+30+48; got != want {
		t.Fatalf("height with sections = %d, want %d", got, want)
	}
}
