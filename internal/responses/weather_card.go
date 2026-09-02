package responses

import "strings"

// WeatherCard is the structured payload for the weather card image. All
// display labels (hour labels, day labels, wind text, meta line) are
// preformatted by the caller; the renderer only handles layout and drawing.
type WeatherCard struct {
	Locations []WeatherCardLocation `json:"locations"`
	Meta      string                `json:"meta,omitempty"`
}

type WeatherCardLocation struct {
	Name    string                 `json:"name"`
	Current WeatherCardCurrent     `json:"current"`
	Hourly  []WeatherCardHourPoint `json:"hourly,omitempty"`
	Daily   []WeatherCardDayPoint  `json:"daily,omitempty"`
	Alerts  []string               `json:"alerts,omitempty"`
}

type WeatherCardCurrent struct {
	Temperature   float64 `json:"temperature"`
	ConditionText string  `json:"conditionText"`
	Icon          string  `json:"icon"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	HasRange      bool    `json:"hasRange"`
	HumidityText  string  `json:"humidityText,omitempty"`
	WindText      string  `json:"windText,omitempty"`
}

type WeatherCardHourPoint struct {
	Label                    string  `json:"label"`
	Temperature              float64 `json:"temperature"`
	PrecipitationProbability float64 `json:"precipitationProbability"`
}

type WeatherCardDayPoint struct {
	Label         string  `json:"label"`
	Low           float64 `json:"low"`
	High          float64 `json:"high"`
	ConditionText string  `json:"conditionText"`
}

// NewWeatherCardImage builds the structured weather card image. altText is the
// plain-text reply used as fallback and cache key material.
func NewWeatherCardImage(card *WeatherCard, altText string) *Image {
	if card == nil || len(card.Locations) == 0 {
		return nil
	}
	altText = strings.TrimSpace(altText)
	if altText == "" {
		return nil
	}
	return &Image{
		Kind:    "weather",
		Title:   "天气",
		AltText: altText,
		Lines:   strings.Split(altText, "\n"),
		Weather: card,
	}
}
