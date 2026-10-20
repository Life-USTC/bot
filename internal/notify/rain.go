package notify

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

// Only timestamped actual Amap conditions establish a rain transition. Hourly
// forecasts, probability, and modeled Open-Meteo current conditions do not.
func rainObservation(snapshot map[string]any, campus string, now time.Time) (time.Time, bool, bool) {
	if lifedata.NestedString(snapshot, "location", "key") != campus {
		return time.Time{}, false, false
	}
	fetched, err := time.Parse(time.RFC3339, lifedata.FirstString(snapshot, "fetchedAt"))
	if err != nil || fetched.After(now) || now.Sub(fetched) >= 15*time.Minute {
		return time.Time{}, false, false
	}
	current, _ := snapshot["current"].(map[string]any)
	if lifedata.FirstString(current, "source") != "amap" {
		return time.Time{}, false, false
	}
	observed, err := time.Parse(time.RFC3339, lifedata.FirstString(current, "observedAt"))
	if err != nil || observed.After(fetched) || observed.After(now) || now.Sub(observed) >= time.Hour {
		return time.Time{}, false, false
	}
	var raining bool
	switch lifedata.NestedString(current, "condition", "text") {
	case "阵雨", "雷阵雨", "雷阵雨并伴有冰雹", "小雨", "中雨", "大雨", "暴雨", "大暴雨", "特大暴雨", "强阵雨", "强雷阵雨", "极端降雨", "毛毛雨/细雨", "雨", "小雨-中雨", "中雨-大雨", "大雨-暴雨", "暴雨-大暴雨", "大暴雨-特大暴雨", "冻雨", "雨夹雪":
		raining = true
	case "晴", "多云", "阴", "少云", "晴间多云", "有风", "平静", "微风", "和风", "清风", "强风/劲风", "疾风", "大风", "烈风", "风暴", "狂爆风", "飓风", "雾", "薄雾", "霾", "扬沙", "浮尘", "沙尘暴", "强沙尘暴", "热", "冷":
		raining = false
	default:
		return time.Time{}, false, false
	}
	return observed, raining, true
}
func (p *Poller) pollRain(ctx context.Context) {
	if p.Store == nil || p.Life == nil {
		return
	}
	settings, err := p.Store.EnabledRainSettings(ctx)
	if err != nil {
		p.logf("list rain settings failed: %v", err)
		return
	}
	if p.rainSession == "" {
		p.rainSession = rand.Text()
	}
	snapshots := make(map[string]map[string]any)
	for _, setting := range settings {
		if !store.IsDirectConversation(setting.Identity) {
			continue
		}
		snapshot, exists := snapshots[setting.Campus]
		if !exists {
			snapshot, err = p.Life.Weather(ctx, setting.Campus)
			if err != nil {
				p.logf("poll rain weather failed: %v", err)
				snapshot = nil
			}
			snapshots[setting.Campus] = snapshot
		}
		now := p.now()
		observed, raining, valid := rainObservation(snapshot, setting.Campus, now)
		session := p.rainSession
		if !valid {
			session = ""
		}
		if err := p.Store.ObserveRain(ctx, setting, session, observed, raining, now); err != nil {
			p.logf("record rain observation failed: %v", err)
		}
	}
}
