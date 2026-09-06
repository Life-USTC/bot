package responses

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"
)

// remoteGridPayload contains schedule semantics for the phone agenda template.
// The card template owns all widths, spacing, font sizes, and text wrapping.
type remoteGridPayload struct {
	Title   string             `json:"title"`
	Summary string             `json:"summary"`
	Days    []remoteGridDay    `json:"days"`
	Periods []remoteGridPeriod `json:"periods"`
	Items   []remoteGridItem   `json:"items,omitempty"`
	Footer  []string           `json:"footer,omitempty"`
}

type remoteGridDay struct {
	Label string `json:"label"`
	Date  string `json:"date,omitempty"`
	Today bool   `json:"today,omitempty"`
}

type remoteGridPeriod struct {
	Label string `json:"label"`
	Time  string `json:"time"`
}

type remoteGridItem struct {
	Day      int    `json:"day"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Period   string `json:"period"`
	Time     string `json:"time"`
	Course   string `json:"course"`
	Location string `json:"location,omitempty"`
	Weeks    string `json:"weeks,omitempty"`
	Color    string `json:"color"`
}

// buildGridRequest mirrors the semantic decisions in renderScheduleGridPNG.
// The Go renderer keeps ownership of validity bounds, colors, and the
// current-day decision; the sidecar lays the resulting agenda out in flow.
func (r RemoteRenderer) buildGridRequest(img *Image) remoteGridPayload {
	grid := img.Grid
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	title := strings.TrimSpace(img.Title)
	if title == "" {
		title = "课表"
	}

	periodRange := "第 1–" + fmt.Sprint(len(grid.Periods)) + " 节"
	summary := "周日–周六 · " + periodRange
	if len(grid.Days) == 1 {
		summary = strings.TrimSpace(grid.Days[0].Label) + " · " + periodRange
	} else if scheduleGridHasNoDates(grid) {
		summary = "整学期 · " + periodRange
	}

	todayIndex := scheduleGridTodayIndex(grid, now)
	req := remoteGridPayload{
		Title:   title,
		Summary: summary,
		Footer: func() []string {
			footer := richFooterLines(now)
			return []string{footer[0], footer[1]}
		}(),
	}
	for i, day := range grid.Days {
		label := strings.TrimSpace(day.Label)
		today := i == todayIndex
		if today && !strings.Contains(label, "今天") {
			label += " · 今天"
		}
		req.Days = append(req.Days, remoteGridDay{
			Label: label,
			Date:  strings.TrimSpace(day.Date),
			Today: today,
		})
	}
	for _, period := range grid.Periods {
		req.Periods = append(req.Periods, remoteGridPeriod{
			Label: strings.TrimSpace(period.Label),
			Time:  strings.TrimSpace(period.Time),
		})
	}

	for _, item := range grid.Items {
		if !scheduleGridItemIsValid(item, len(grid.Days), len(grid.Periods)) {
			continue
		}
		period, itemTime := scheduleGridItemPeriodAndTime(grid.Periods, item.StartPeriod, item.EndPeriod)
		req.Items = append(req.Items, remoteGridItem{
			Day:      item.Day,
			Start:    item.StartPeriod,
			End:      item.EndPeriod,
			Period:   period,
			Time:     itemTime,
			Course:   strings.TrimSpace(item.Course),
			Location: strings.TrimSpace(item.Location),
			Weeks:    strings.TrimSpace(item.Weeks),
			Color:    scheduleGridColorHex(scheduleGridCourseColor(item)),
		})
	}
	sort.SliceStable(req.Items, func(i, j int) bool {
		left, right := req.Items[i], req.Items[j]
		if left.Day != right.Day {
			return left.Day < right.Day
		}
		if left.Start != right.Start {
			return left.Start < right.Start
		}
		return left.End < right.End
	})
	return req
}

func scheduleGridItemIsValid(item ScheduleGridItem, dayCount, periodCount int) bool {
	return item.Day >= 0 && item.Day < dayCount &&
		item.StartPeriod >= 1 && item.EndPeriod >= item.StartPeriod &&
		item.EndPeriod <= periodCount
}

func scheduleGridItemPeriodAndTime(periods []ScheduleGridPeriod, start, end int) (string, string) {
	if start < 1 || end < start || end > len(periods) {
		return "", ""
	}
	first := periods[start-1]
	last := periods[end-1]
	period := strings.TrimSpace(first.Label)
	if start != end {
		lastLabel := strings.TrimSpace(last.Label)
		if period == "" {
			period = lastLabel
		} else if lastLabel != "" && lastLabel != period {
			period += "–" + lastLabel
		}
	}
	return period, scheduleGridTimeRange(first.Time, last.Time)
}

// scheduleGridTimeRange keeps the first start and last end from the period
// labels. It only reformats the supplied values; it never invents a clock
// time when a period contains a non-time label.
func scheduleGridTimeRange(first, last string) string {
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)
	if first == "" {
		return last
	}
	if last == "" || first == last {
		return first
	}
	start := scheduleGridTimeEndpoint(first, false)
	end := scheduleGridTimeEndpoint(last, true)
	if start == "" {
		start = first
	}
	if end == "" {
		end = last
	}
	if start == end {
		return start
	}
	return start + "–" + end
}

func scheduleGridTimeEndpoint(value string, last bool) string {
	for _, separator := range []string{"–", "—", "-", "~", "～", "至"} {
		var index int
		if last {
			index = strings.LastIndex(value, separator)
		} else {
			index = strings.Index(value, separator)
		}
		if index >= 0 {
			if last {
				return strings.TrimSpace(value[index+len(separator):])
			}
			return strings.TrimSpace(value[:index])
		}
	}
	return strings.TrimSpace(value)
}

func scheduleGridColorHex(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
