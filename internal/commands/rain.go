package commands

import (
	"context"
	"github.com/Life-USTC/Bot/internal/store"
)

func (h Handler) rainSettings(ctx context.Context, ident store.Identity, args []string) (store.RainSettings, error) {
	settings, err := h.Store.RainSettings(ctx, ident)
	if err != nil {
		return settings, err
	}
	settings.Identity = ident
	if len(args) > 0 && args[0] == "rain" && len(args) >= 2 {
		settings.Enabled = args[1] == "on"
		if len(args) == 3 {
			settings.Campus, _ = weatherLocationFilter(args[2:])
		}
		err = h.Store.SaveRainSettings(ctx, settings)
	} else if len(args) == 1 && args[0] == "off" {
		settings.Enabled = false
		err = h.Store.SaveRainSettings(ctx, settings)
	}
	return settings, err
}
func formatRainSettings(settings store.RainSettings) string {
	campus := "本部"
	if settings.Campus == "ustc-gaoxin" {
		campus = "高新校区"
	}
	return "降雨提醒：" + onOffText(settings.Enabled) + "（" + campus + "，按高德实况观测变化）"
}
