package responses

import (
	"fmt"
	"image/color"
	"strings"
	"time"
)

// remoteGridPayload contains all geometry and semantic decisions needed by
// the Typst grid template. Coordinates remain logical points; renderd applies
// the requested rasterization scale.
type remoteGridPayload struct {
	Title        string             `json:"title"`
	Summary      string             `json:"summary"`
	Days         []remoteGridDay    `json:"days"`
	Periods      []remoteGridPeriod `json:"periods"`
	DayWidth     int                `json:"day_width"`
	LabelWidth   int                `json:"label_width"`
	RowHeight    int                `json:"row_height"`
	HeaderHeight int                `json:"header_height"`
	Dividers     []int              `json:"dividers,omitempty"`
	Items        []remoteGridItem   `json:"items,omitempty"`
	Footer       []string           `json:"footer,omitempty"`
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
	Day        int    `json:"day"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Course     string `json:"course"`
	Location   string `json:"location,omitempty"`
	Weeks      string `json:"weeks,omitempty"`
	Color      string `json:"color"`
	Large      bool   `json:"large,omitempty"`
	CourseSize int    `json:"course_size"`
	MetaSize   int    `json:"meta_size"`
}

// buildGridRequest mirrors renderScheduleGridPNG. The Go renderer keeps
// ownership of bounds, truncation, colors, and the current-day decision; the
// sidecar only paints the resulting fixed geometry.
func (r RemoteRenderer) buildGridRequest(img *Image) remoteGridPayload {
	grid := img.Grid
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	metrics := defaultScheduleGridMetrics(len(grid.Days), len(grid.Periods))
	title := strings.TrimSpace(img.Title)
	if title == "" {
		title = "课表"
	}

	periodRange := "第 1–" + fmt.Sprint(len(grid.Periods)) + " 节"
	summary := "周日–周六 · " + periodRange
	if len(grid.Days) == 1 {
		summary = grid.Days[0].Label + " · " + periodRange
	} else if scheduleGridHasNoDates(grid) {
		summary = "整学期 · " + periodRange
	}

	todayIndex := scheduleGridTodayIndex(grid, now)
	req := remoteGridPayload{
		Title:        title,
		Summary:      summary,
		DayWidth:     metrics.DayWidth,
		LabelWidth:   metrics.LabelWidth,
		RowHeight:    metrics.RowHeight,
		HeaderHeight: metrics.HeaderHeight,
		Footer: func() []string {
			footer := richFooterLines(now)
			return []string{footer[0], footer[1]}
		}(),
	}
	for i, day := range grid.Days {
		label := day.Label
		today := i == todayIndex
		if today {
			label += " · 今天"
		}
		req.Days = append(req.Days, remoteGridDay{
			Label: label,
			Date:  day.Date,
			Today: today,
		})
	}
	for _, period := range grid.Periods {
		req.Periods = append(req.Periods, remoteGridPeriod{
			Label: period.Label,
			Time:  period.Time,
		})
	}
	for _, boundary := range []int{5, 10} {
		if _, ok := scheduleGridDividerBounds(boundary, metrics); ok {
			req.Dividers = append(req.Dividers, boundary)
		}
	}

	maxTextWidth := metrics.DayWidth - 12
	for _, item := range grid.Items {
		rect, ok := scheduleGridItemBounds(item, metrics)
		if !ok {
			continue
		}
		large := scheduleGridItemUsesLargeText(rect, metrics)
		courseSize := scheduleGridCourseFontSize
		metaSize := scheduleGridMetaFontSize
		if large {
			if richTextWidth(strings.TrimSpace(item.Course), scheduleGridLargeCourseFontSize) <= maxTextWidth {
				courseSize = scheduleGridLargeCourseFontSize
			}
			if richTextWidth(strings.TrimSpace(item.Location), scheduleGridLargeMetaFontSize) <= maxTextWidth &&
				richTextWidth(strings.TrimSpace(item.Weeks), scheduleGridLargeMetaFontSize) <= maxTextWidth {
				metaSize = scheduleGridLargeMetaFontSize
			}
		}
		req.Items = append(req.Items, remoteGridItem{
			Day:        item.Day,
			Start:      item.StartPeriod,
			End:        item.EndPeriod,
			Course:     fitScheduleGridText(item.Course, maxTextWidth, courseSize),
			Location:   fitScheduleGridText(item.Location, maxTextWidth, metaSize),
			Weeks:      fitScheduleGridText(item.Weeks, maxTextWidth, metaSize),
			Color:      scheduleGridColorHex(scheduleGridCourseColor(item)),
			Large:      large,
			CourseSize: courseSize,
			MetaSize:   metaSize,
		})
	}
	return req
}

func scheduleGridColorHex(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
