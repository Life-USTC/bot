package responses

import (
	"errors"
	"time"
)

// remoteWeatherPayload is intentionally a flat, renderer-facing view of the
// structured weather card. The sidecar receives the same labels and values the
// legacy renderer displays; page width and height belong to the shared Typst
// card style and are therefore not part of this payload.
type remoteWeatherPayload struct {
	Title     string                `json:"title"`
	Meta      string                `json:"meta,omitempty"`
	Footer    []string              `json:"footer,omitempty"`
	Locations []WeatherCardLocation `json:"locations"`
}

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
	footer := richFooterLines(now, img.Ref)
	return remoteWeatherPayload{
		// The legacy weather renderer always uses the fixed card title,
		// regardless of the caller-provided image title.
		Title:     "天气",
		Meta:      img.Weather.Meta,
		Footer:    []string{footer[0], footer[1]},
		Locations: append([]WeatherCardLocation(nil), img.Weather.Locations...),
	}, nil
}
