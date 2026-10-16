// Schedule grid semantics: which column is today, and the stable colour a
// course keeps across cards. The timetable itself is drawn by renderd.

package responses

import (
	"crypto/sha256"
	"image/color"
	"math"
	"strings"
	"time"
)

func scheduleGridTodayIndex(grid *ScheduleGrid, now time.Time) int {
	if grid == nil {
		return -1
	}
	today := now.Format("01-02")
	for i, day := range grid.Days {
		if strings.TrimSpace(day.Date) == today {
			return i
		}
	}
	return -1
}

func scheduleGridCourseColor(item ScheduleGridItem) color.RGBA {
	key := normalizeScheduleGridCourseKey(item.SectionKey)
	if key == "" {
		key = normalizeScheduleGridCourseKey(item.Course)
	}
	return generateSectionColor(key)
}

func generateSectionColor(key string) color.RGBA {
	key = normalizeScheduleGridCourseKey(key)
	if key == "" {
		return color.RGBA{226, 232, 240, 255}
	}
	sum := sha256.Sum256([]byte(key))
	hue := float64(uint16(sum[0])<<8|uint16(sum[1])) / 65535 * 360
	saturation := 0.42 + float64(sum[2])/255*0.14
	lightness := 0.86 + float64(sum[3])/255*0.05
	return hslColor(hue, saturation, lightness)
}

func normalizeScheduleGridCourseKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func hslColor(hue, saturation, lightness float64) color.RGBA {
	chroma := (1 - math.Abs(2*lightness-1)) * saturation
	sector := math.Mod(hue/60, 6)
	secondary := chroma * (1 - math.Abs(math.Mod(sector, 2)-1))
	var red, green, blue float64
	switch int(sector) {
	case 0:
		red, green = chroma, secondary
	case 1:
		red, green = secondary, chroma
	case 2:
		green, blue = chroma, secondary
	case 3:
		green, blue = secondary, chroma
	case 4:
		red, blue = secondary, chroma
	default:
		red, blue = chroma, secondary
	}
	match := lightness - chroma/2
	channel := func(value float64) uint8 { return uint8(math.Round((value + match) * 255)) }
	return color.RGBA{channel(red), channel(green), channel(blue), 255}
}
