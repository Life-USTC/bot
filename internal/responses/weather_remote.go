package responses

import (
	"errors"
	"time"
)

// remoteWeatherPayload is intentionally a flat, renderer-facing view of the
// structured weather card. The sidecar receives the same labels and values the
// legacy renderer displays; only fixed canvas geometry is added so the Typst
// template can preserve the legacy pixel dimensions.
type remoteWeatherPayload struct {
	Title       string                `json:"title"`
	Meta        string                `json:"meta,omitempty"`
	Footer      []string              `json:"footer,omitempty"`
	CanvasWidth int                   `json:"canvas_width"`
	Height      int                   `json:"height"`
	Locations   []WeatherCardLocation `json:"locations"`
}

const remoteWeatherCanvasWidth = 920

// buildWeatherRequest converts the structured image into the sidecar
// envelope payload. It does not call the legacy renderer and therefore keeps
// this path deterministic when RemoteRenderer.Now is overridden in tests.
func (r RemoteRenderer) buildWeatherRequest(img *Image) (remoteWeatherPayload, error) {
	if img == nil || img.Weather == nil {
		return remoteWeatherPayload{}, errors.New("response weather card is empty")
	}
	if len(img.Weather.Locations) == 0 {
		return remoteWeatherPayload{}, errors.New("response weather card has no locations")
	}
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	footer := richFooterLines(now)
	return remoteWeatherPayload{
		// The legacy weather renderer always uses the fixed card title,
		// regardless of the caller-provided image title.
		Title:       "天气",
		Meta:        img.Weather.Meta,
		Footer:      []string{footer[0], footer[1]},
		CanvasWidth: remoteWeatherCanvasWidth,
		Height:      remoteWeatherLogicalHeight(img.Weather),
		Locations:   append([]WeatherCardLocation(nil), img.Weather.Locations...),
	}, nil
}

// Keep the same fixed metrics and trailing gap as renderWeatherCardPNG. The
// sidecar validates this value, which prevents a caller from accidentally
// changing the page dimensions without changing the legacy layout contract.
func remoteWeatherLogicalHeight(card *WeatherCard) int {
	if card == nil {
		return 0
	}
	height := 52 + 12
	for _, location := range card.Locations {
		locationHeight := 34 + 110
		if location.Current.HumidityText != "" || location.Current.WindText != "" {
			locationHeight += 68
		}
		if len(location.Hourly) > 0 {
			locationHeight += 32 + 158 + 20
		}
		if len(location.Daily) > 0 {
			locationHeight += 32 + 30*len(location.Daily)
		}
		if len(location.Alerts) > 0 {
			locationHeight += 32 + 24*len(location.Alerts)
		}
		height += locationHeight + 30
	}
	return height + 48
}
