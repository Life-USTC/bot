package responses

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type scheduleGridMetrics struct {
	Scale         int
	MarginX       int
	TitleBaseline int
	GridTop       int
	HeaderHeight  int
	RowHeight     int
	LabelWidth    int
	DayWidth      int
	DayCount      int
	PeriodCount   int
	FooterGap     int
	BottomMargin  int
}

const scheduleGridDividerThickness = 4

var scheduleGridCourseBackgrounds = [...]color.RGBA{
	{224, 242, 254, 255},
	{237, 233, 254, 255},
	{220, 252, 231, 255},
	{254, 243, 199, 255},
	{255, 228, 230, 255},
	{224, 231, 255, 255},
	{204, 251, 241, 255},
}

func defaultScheduleGridMetrics(dayCount, periodCount int) scheduleGridMetrics {
	dayWidth := 156
	if dayCount == 1 {
		dayWidth = 360
	}
	return scheduleGridMetrics{
		Scale:         2,
		MarginX:       36,
		TitleBaseline: 54,
		GridTop:       82,
		HeaderHeight:  54,
		RowHeight:     56,
		LabelWidth:    120,
		DayWidth:      dayWidth,
		DayCount:      dayCount,
		PeriodCount:   periodCount,
		FooterGap:     28,
		BottomMargin:  28,
	}
}

func (m scheduleGridMetrics) gridWidth() int {
	return m.LabelWidth + m.DayCount*m.DayWidth
}

func (m scheduleGridMetrics) gridBottom() int {
	return m.GridTop + m.HeaderHeight + m.PeriodCount*m.RowHeight
}

func (m scheduleGridMetrics) space() imageRenderSpace {
	return imageRenderSpace{
		Width:  2*m.MarginX + m.gridWidth(),
		Height: m.gridBottom() + m.FooterGap + m.BottomMargin,
		Scale:  m.Scale,
	}
}

func scheduleGridItemBounds(item ScheduleGridItem, metrics scheduleGridMetrics) (image.Rectangle, bool) {
	if item.Day < 0 || item.Day >= metrics.DayCount ||
		item.StartPeriod < 1 || item.EndPeriod < item.StartPeriod || item.EndPeriod > metrics.PeriodCount {
		return image.Rectangle{}, false
	}
	left := metrics.MarginX + metrics.LabelWidth + item.Day*metrics.DayWidth
	top := metrics.GridTop + metrics.HeaderHeight + (item.StartPeriod-1)*metrics.RowHeight
	return image.Rect(left, top, left+metrics.DayWidth, top+(item.EndPeriod-item.StartPeriod+1)*metrics.RowHeight), true
}

func scheduleGridDividerBounds(boundary int, metrics scheduleGridMetrics) (image.Rectangle, bool) {
	if boundary <= 0 || boundary >= metrics.PeriodCount {
		return image.Rectangle{}, false
	}
	y := metrics.GridTop + metrics.HeaderHeight + boundary*metrics.RowHeight
	top := y - scheduleGridDividerThickness/2
	return image.Rect(metrics.MarginX, top, metrics.MarginX+metrics.gridWidth(), top+scheduleGridDividerThickness), true
}

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
	key := normalizeScheduleGridCourseKey(item.CourseID)
	if key == "" {
		key = normalizeScheduleGridCourseKey(item.Course)
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	return scheduleGridCourseBackgrounds[int(hash.Sum32()%uint32(len(scheduleGridCourseBackgrounds)))]
}

func normalizeScheduleGridCourseKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func (r Renderer) renderScheduleGridPNG(title string, grid *ScheduleGrid) ([]byte, int, int, error) {
	metrics := defaultScheduleGridMetrics(len(grid.Days), len(grid.Periods))
	space := metrics.space()
	s := space.px
	now := time.Now().In(time.FixedZone("CST", 8*60*60))
	todayIndex := scheduleGridTodayIndex(grid, now)
	faces, err := r.richFaces(space.Scale)
	if err != nil {
		return nil, 0, 0, err
	}

	canvas := image.NewRGBA(space.bounds())
	background := color.RGBA{250, 250, 250, 255}
	headerBackground := color.RGBA{244, 244, 245, 255}
	alternateBackground := color.RGBA{248, 250, 252, 255}
	todayHeaderBackground := color.RGBA{204, 251, 241, 255}
	todayBackground := color.RGBA{240, 253, 250, 255}
	ink := color.RGBA{39, 39, 42, 255}
	muted := color.RGBA{113, 113, 122, 255}
	line := color.RGBA{203, 213, 225, 255}
	divider := color.RGBA{100, 116, 139, 255}
	accent := color.RGBA{15, 118, 110, 255}
	drawRect(canvas, canvas.Bounds(), background)
	drawBusLogoWatermark(canvas, canvas.Bounds(), s(120), 0.10)
	if strings.TrimSpace(title) == "" {
		title = "课表"
	}
	drawMixedText(canvas, faces.Title, faces.TitleMono, s(metrics.MarginX), s(metrics.TitleBaseline), title, ink)
	summary := "周日–周六 · 第 1–" + strconv.Itoa(len(grid.Periods)) + " 节"
	if len(grid.Days) == 1 {
		summary = grid.Days[0].Label + " · 第 1–" + strconv.Itoa(len(grid.Periods)) + " 节"
	}
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, s(space.Width-metrics.MarginX), s(metrics.TitleBaseline), summary, muted)

	gridLeft := metrics.MarginX
	drawScheduleGridCell(canvas, image.Rect(gridLeft, metrics.GridTop, gridLeft+metrics.LabelWidth, metrics.GridTop+metrics.HeaderHeight), s, headerBackground, line)
	drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, s(gridLeft+metrics.LabelWidth/2), s(metrics.GridTop+33), "节次", ink)

	for dayIndex, day := range grid.Days {
		left := metrics.MarginX + metrics.LabelWidth + dayIndex*metrics.DayWidth
		rect := image.Rect(left, metrics.GridTop, left+metrics.DayWidth, metrics.GridTop+metrics.HeaderHeight)
		cellBackground := headerBackground
		headerText := day.Label
		headerColor := ink
		dateColor := muted
		if dayIndex == todayIndex {
			cellBackground = todayHeaderBackground
			headerText += " · 今天"
			headerColor = accent
			dateColor = accent
		}
		drawScheduleGridCell(canvas, rect, s, cellBackground, line)
		drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, s(rect.Min.X+metrics.DayWidth/2), s(rect.Min.Y+23), headerText, headerColor)
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(rect.Min.X+metrics.DayWidth/2), s(rect.Min.Y+43), day.Date, dateColor)
	}

	for periodIndex, period := range grid.Periods {
		top := metrics.GridTop + metrics.HeaderHeight + periodIndex*metrics.RowHeight
		rowBackground := background
		if periodIndex%2 == 1 {
			rowBackground = alternateBackground
		}
		labelRect := image.Rect(metrics.MarginX, top, metrics.MarginX+metrics.LabelWidth, top+metrics.RowHeight)
		drawScheduleGridCell(canvas, labelRect, s, rowBackground, line)
		drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, s(labelRect.Min.X+metrics.LabelWidth/2), s(labelRect.Min.Y+22), period.Label, ink)
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(labelRect.Min.X+metrics.LabelWidth/2), s(labelRect.Min.Y+43), period.Time, muted)
		for dayIndex := range grid.Days {
			left := metrics.MarginX + metrics.LabelWidth + dayIndex*metrics.DayWidth
			cellBackground := rowBackground
			if dayIndex == todayIndex {
				cellBackground = todayBackground
			}
			drawScheduleGridCell(canvas, image.Rect(left, top, left+metrics.DayWidth, top+metrics.RowHeight), s, cellBackground, line)
		}
	}

	for _, item := range grid.Items {
		rect, ok := scheduleGridItemBounds(item, metrics)
		if !ok {
			continue
		}
		fill := scheduleGridCourseColor(item)
		scaled := image.Rect(s(rect.Min.X+1), s(rect.Min.Y+1), s(rect.Max.X), s(rect.Max.Y))
		drawRect(canvas, scaled, fill)
		drawScheduleGridBorder(canvas, rect, s, accent)

		centerX := s(rect.Min.X + rect.Dx()/2)
		centerY := s(rect.Min.Y + rect.Dy()/2)
		course := fitScheduleGridText(item.Course, metrics.DayWidth-12, 13)
		location := fitScheduleGridText(item.Location, metrics.DayWidth-12, richMetaFontSize)
		if location == "" {
			drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, centerX, centerY+s(5), course, ink)
			continue
		}
		drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, centerX, centerY-s(3), course, ink)
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, centerX, centerY+s(16), location, muted)
	}

	if todayIndex >= 0 {
		left := metrics.MarginX + metrics.LabelWidth + todayIndex*metrics.DayWidth
		top := metrics.GridTop
		bottom := metrics.gridBottom()
		thickness := s(2)
		drawRect(canvas, image.Rect(s(left), s(top), s(left)+thickness, s(bottom)), accent)
		drawRect(canvas, image.Rect(s(left+metrics.DayWidth)-thickness, s(top), s(left+metrics.DayWidth), s(bottom)), accent)
	}
	for _, boundary := range []int{5, 10} {
		rect, ok := scheduleGridDividerBounds(boundary, metrics)
		if !ok {
			continue
		}
		drawRect(canvas, image.Rect(s(rect.Min.X), s(rect.Min.Y), s(rect.Max.X), s(rect.Max.Y)), divider)
	}

	footerY := s(metrics.gridBottom() + metrics.FooterGap)
	footerLines := richFooterLines(now)
	right := s(space.Width - metrics.MarginX)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, footerY, footerLines[0], muted)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, footerY+s(14), footerLines[1], muted)

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buffer.Bytes(), canvas.Bounds().Dx(), canvas.Bounds().Dy(), nil
}

func drawScheduleGridCell(dst *image.RGBA, rect image.Rectangle, scale func(int) int, fill, border color.RGBA) {
	scaled := image.Rect(scale(rect.Min.X), scale(rect.Min.Y), scale(rect.Max.X), scale(rect.Max.Y))
	drawRect(dst, scaled, fill)
	drawScheduleGridBorder(dst, rect, scale, border)
}

func drawScheduleGridBorder(dst *image.RGBA, rect image.Rectangle, scale func(int) int, border color.RGBA) {
	left, top := scale(rect.Min.X), scale(rect.Min.Y)
	right, bottom := scale(rect.Max.X), scale(rect.Max.Y)
	thickness := scale(1)
	drawRect(dst, image.Rect(left, top, right, top+thickness), border)
	drawRect(dst, image.Rect(left, bottom-thickness, right, bottom), border)
	drawRect(dst, image.Rect(left, top, left+thickness, bottom), border)
	drawRect(dst, image.Rect(right-thickness, top, right, bottom), border)
}

func fitScheduleGridText(value string, maxWidth, fontSize int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || richTextWidth(value, fontSize) <= maxWidth {
		return value
	}
	// Drop a trailing parenthetical whole rather than cutting it in half.
	if head := scheduleGridHeadBeforeTrailingBracket(value); head != "" && richTextWidth(head, fontSize) <= maxWidth {
		return head
	}
	runes := []rune(value)
	cut := len(runes)
	for cut > 0 && richTextWidth(string(runes[:cut])+"…", fontSize) > maxWidth {
		cut--
	}
	// Never leave the cut dangling inside an unclosed bracket pair.
	if open := scheduleGridUnclosedBracket(runes[:cut]); open >= 0 {
		cut = open
	}
	// Cut Latin text at the last word boundary instead of mid-word.
	if cut > 0 && cut < len(runes) && isScheduleGridLatinWordRune(runes[cut-1]) && isScheduleGridLatinWordRune(runes[cut]) {
		for i := cut - 1; i > 0; i-- {
			if runes[i] == ' ' {
				cut = i
				break
			}
		}
	}
	kept := runes[:cut]
	for len(kept) > 0 {
		switch kept[len(kept)-1] {
		case ' ', '/', '（', '(':
			kept = kept[:len(kept)-1]
		default:
			return string(kept) + "…"
		}
	}
	return "…"
}

// scheduleGridHeadBeforeTrailingBracket returns value without its trailing
// parenthetical, or "" when value does not end with one.
func scheduleGridHeadBeforeTrailingBracket(value string) string {
	runes := []rune(value)
	for i := len(runes) - 2; i > 0; i-- {
		var close rune
		switch runes[i] {
		case '（':
			close = '）'
		case '(':
			close = ')'
		default:
			continue
		}
		if runes[len(runes)-1] != close {
			return ""
		}
		return strings.TrimRight(string(runes[:i]), " /")
	}
	return ""
}

// scheduleGridUnclosedBracket returns the index of the outermost opening
// bracket that is never closed within runes, or -1 when all pairs balance.
func scheduleGridUnclosedBracket(runes []rune) int {
	open := -1
	depth := 0
	for i, r := range runes {
		switch r {
		case '（', '(':
			if depth == 0 {
				open = i
			}
			depth++
		case '）', ')':
			if depth > 0 {
				depth--
			}
		}
	}
	if depth > 0 {
		return open
	}
	return -1
}

func isScheduleGridLatinWordRune(r rune) bool {
	return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
