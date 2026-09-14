package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type weatherLocationSpec struct {
	key  string
	name string
}

var weatherLocations = []weatherLocationSpec{
	{key: "ustc-main", name: "本部"},
	{key: "ustc-gaoxin", name: "高新校区"},
}

func weatherLocationFilter(args []string) (string, bool) {
	if len(args) != 1 {
		return "", len(args) == 0
	}
	switch normToken(args[0]) {
	case "本部", "主校区", "main":
		return "ustc-main", true
	case "高新", "高新校区", "高新区", "高新园区", "gaoxin", "gx":
		return "ustc-gaoxin", true
	}
	return "", false
}

func weatherInput(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	_, ok := weatherLocationFilter(args)
	return ok
}

func (h Handler) weather(ctx context.Context, args []string) string {
	text, _ := h.weatherReport(ctx, args)
	return text
}

func weatherExecutor(h Handler, ctx context.Context, _ store.Identity, inv Invocation) CapabilityOutcome {
	if h.execution == nil {
		h.execution = &capabilityExecutionState{}
	}
	text, card := h.weatherReport(ctx, inv.Args)
	outcome := outcomeFromResponse(h, Response{Text: text, Kind: inv.Name})
	if card != nil && h.EnableImageResponses && outcome.Status == CapabilityOutcomeSuccess {
		outcome.Response.Image = responses.NewWeatherCardImage(card, textutil.PlainMonospace(text))
	}
	return outcome
}

func (h Handler) weatherReport(ctx context.Context, args []string) (string, *responses.WeatherCard) {
	filter, ok := weatherLocationFilter(args)
	if !ok {
		return h.invalidInput("想查哪个校区？例如：天气 高新"), nil
	}
	lines := []string{"天气："}
	card := &responses.WeatherCard{}
	latestFetched := ""
	providers := []string{}
	data := make(map[string]any)
	for _, location := range weatherLocations {
		if filter != "" && location.key != filter {
			continue
		}
		snapshot, err := h.Life.Weather(ctx, location.key)
		if err != nil {
			return h.commandError("天气查不到：", err), nil
		}
		data[location.key] = snapshot
		lines = append(lines, "")
		lines = append(lines, formatWeatherLocation(location.name, snapshot)...)
		card.Locations = append(card.Locations, weatherCardLocation(location.name, snapshot))
		if fetched := lifedata.FirstString(snapshot, "fetchedAt"); fetched != "" {
			if latestFetched == "" {
				latestFetched = fetched
			} else if next, nextOK := lifedata.ParseAPITime(fetched); nextOK {
				if current, currentOK := lifedata.ParseAPITime(latestFetched); !currentOK || next.After(current) {
					latestFetched = fetched
				}
			}
		}
		for _, provider := range weatherProviders(snapshot) {
			if !containsString(providers, provider) {
				providers = append(providers, provider)
			}
		}
	}
	meta := []string{}
	if parsed, ok := lifedata.ParseAPITime(latestFetched); ok {
		meta = append(meta, "更新于 "+parsed.In(lifedata.ChinaLocation()).Format("15:04"))
	}
	if len(providers) > 0 {
		meta = append(meta, "数据来源："+strings.Join(providers, "、"))
	}
	if len(meta) > 0 {
		lines = append(lines, "", strings.Join(meta, " · "))
		card.Meta = strings.Join(meta, " · ")
	}
	h.markData(map[string]any{
		"operation": "weather",
		"locations": data,
		"filter":    filter,
	})
	return textutil.MonospaceDigits(strings.Join(lines, "\n")), card
}

func weatherFloat(m map[string]any, key string) (float64, bool) {
	value, ok := m[key].(float64)
	return value, ok
}

func weatherCardLocation(name string, snapshot map[string]any) responses.WeatherCardLocation {
	location := responses.WeatherCardLocation{Name: name}
	current := weatherMap(snapshot, "current")
	if len(current) == 0 {
		return location
	}
	temperature, _ := weatherFloat(current, "temperature")
	location.Current = responses.WeatherCardCurrent{
		Temperature:   temperature,
		ConditionText: lifedata.NestedString(current, "condition", "text"),
		Icon:          lifedata.NestedString(current, "condition", "icon"),
	}
	if humidity := lifedata.FirstString(current, "humidity"); humidity != "" {
		location.Current.HumidityText = humidity + "%"
	}
	windDirection := lifedata.FirstString(current, "windDirection")
	windSpeed := lifedata.FirstString(current, "windSpeed")
	if windDirection != "" && windSpeed != "" {
		location.Current.WindText = windDirection + "风 " + windSpeed + " 级"
	}

	daily := weatherList(snapshot, "daily")
	if len(daily) > 0 {
		if low, ok := weatherFloat(daily[0], "temperatureLow"); ok {
			if high, ok := weatherFloat(daily[0], "temperatureHigh"); ok {
				location.Current.Low = low
				location.Current.High = high
				location.Current.HasRange = true
			}
		}
	}

	now := time.Now()
	for _, hour := range weatherList(snapshot, "hourly") {
		at, ok := lifedata.ParseAPITime(lifedata.FirstString(hour, "at"))
		if !ok || at.Before(now) {
			continue
		}
		temperature, ok := weatherFloat(hour, "temperature")
		if !ok {
			continue
		}
		probability, _ := weatherFloat(hour, "precipitationProbability")
		location.Hourly = append(location.Hourly, responses.WeatherCardHourPoint{
			Label:                    at.In(lifedata.ChinaLocation()).Format("15:04"),
			Temperature:              temperature,
			PrecipitationProbability: probability,
		})
		if len(location.Hourly) >= 24 {
			break
		}
	}

	if len(daily) > 4 {
		daily = daily[:4]
	}
	for i, day := range daily {
		date, ok := lifedata.ParseAPITime(lifedata.FirstString(day, "date"))
		if !ok {
			continue
		}
		low, lowOK := weatherFloat(day, "temperatureLow")
		high, highOK := weatherFloat(day, "temperatureHigh")
		if !lowOK || !highOK {
			continue
		}
		label := chineseWeekday(date.In(lifedata.ChinaLocation()))
		if i == 0 {
			label = "今天"
		}
		location.Daily = append(location.Daily, responses.WeatherCardDayPoint{
			Label:         label,
			Low:           low,
			High:          high,
			ConditionText: lifedata.NestedString(day, "condition", "text"),
		})
	}

	location.Alerts = weatherAlerts(snapshot)
	return location
}

func formatWeatherLocation(name string, snapshot map[string]any) []string {
	lines := []string{name + "："}
	current := weatherMap(snapshot, "current")
	if len(current) == 0 {
		return append(lines, "暂无数据。")
	}
	temperature := weatherTemperature(current, "temperature")
	condition := lifedata.NestedString(current, "condition", "text")
	line := strings.TrimSpace(strings.Join(textutil.NonEmpty(temperature, condition), " "))
	if today := weatherTodayRange(snapshot); today != "" {
		line = strings.TrimSpace(line + "（" + today + "）")
	}
	lines = append(lines, line)

	details := []string{}
	if humidity := lifedata.FirstString(current, "humidity"); humidity != "" {
		details = append(details, "湿度 "+humidity+"%")
	}
	windDirection := lifedata.FirstString(current, "windDirection")
	windSpeed := lifedata.FirstString(current, "windSpeed")
	if windDirection != "" && windSpeed != "" {
		details = append(details, windDirection+"风 "+windSpeed+" 级")
	}
	if len(details) > 0 {
		lines = append(lines, strings.Join(details, " · "))
	}

	if hourly := formatWeatherHourly(snapshot); hourly != "" {
		lines = append(lines, "逐小时："+hourly)
	}
	if daily := formatWeatherDaily(snapshot); daily != "" {
		lines = append(lines, "每日："+daily)
	}
	for _, alert := range weatherAlerts(snapshot) {
		lines = append(lines, "预警："+alert)
	}
	return lines
}

func weatherMap(m map[string]any, key string) map[string]any {
	child, _ := m[key].(map[string]any)
	return child
}

func weatherList(m map[string]any, key string) []map[string]any {
	items, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func weatherTemperature(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(float64); ok {
			return fmt.Sprintf("%.0f°", value)
		}
	}
	return ""
}

func weatherTodayRange(snapshot map[string]any) string {
	daily := weatherList(snapshot, "daily")
	if len(daily) == 0 {
		return ""
	}
	low := weatherTemperature(daily[0], "temperatureLow")
	high := weatherTemperature(daily[0], "temperatureHigh")
	if low == "" || high == "" {
		return ""
	}
	return high + " / " + low
}

func formatWeatherHourly(snapshot map[string]any) string {
	hourly := weatherList(snapshot, "hourly")
	now := time.Now()
	parts := []string{}
	for _, hour := range hourly {
		at, ok := lifedata.ParseAPITime(lifedata.FirstString(hour, "at"))
		if !ok || at.Before(now) {
			continue
		}
		part := strings.TrimSpace(strings.Join(textutil.NonEmpty(
			at.In(lifedata.ChinaLocation()).Format("15:04"),
			lifedata.NestedString(hour, "condition", "text"),
			weatherTemperature(hour, "temperature"),
		), " "))
		if probability, ok := hour["precipitationProbability"].(float64); ok && probability > 0 {
			part += fmt.Sprintf(" 降水%.0f%%", probability)
		}
		parts = append(parts, part)
		if len(parts) >= 4 {
			break
		}
	}
	return strings.Join(parts, "、")
}

func formatWeatherDaily(snapshot map[string]any) string {
	daily := weatherList(snapshot, "daily")
	if len(daily) > 4 {
		daily = daily[:4]
	}
	parts := []string{}
	for i, day := range daily {
		date, ok := lifedata.ParseAPITime(lifedata.FirstString(day, "date"))
		if !ok {
			continue
		}
		label := chineseWeekday(date.In(lifedata.ChinaLocation()))
		if i == 0 {
			label = "今天"
		}
		part := strings.TrimSpace(strings.Join(textutil.NonEmpty(
			label,
			weatherRangeText(day),
			lifedata.NestedString(day, "condition", "text"),
		), " "))
		parts = append(parts, part)
	}
	return strings.Join(parts, "、")
}

func weatherRangeText(day map[string]any) string {
	low := weatherTemperature(day, "temperatureLow")
	high := weatherTemperature(day, "temperatureHigh")
	if low == "" || high == "" {
		return ""
	}
	return low + "~" + high
}

func weatherAlerts(snapshot map[string]any) []string {
	alerts := []string{}
	for _, alert := range weatherList(snapshot, "alerts") {
		title := lifedata.FirstString(alert, "title")
		if title == "" {
			continue
		}
		if level := lifedata.FirstString(alert, "level"); level != "" {
			title += "（" + level + "）"
		}
		alerts = append(alerts, title)
	}
	return alerts
}

func weatherProviders(snapshot map[string]any) []string {
	items, _ := snapshot["providers"].([]any)
	providers := make([]string, 0, len(items))
	for _, item := range items {
		if provider, ok := item.(string); ok && strings.TrimSpace(provider) != "" {
			providers = append(providers, strings.TrimSpace(provider))
		}
	}
	return providers
}

func chineseWeekday(t time.Time) string {
	names := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return names[t.Weekday()]
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
