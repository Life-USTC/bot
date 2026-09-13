package responses

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/font"
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

const (
	scheduleGridCourseFontSize      = 14
	scheduleGridMetaFontSize        = 10
	scheduleGridLargeCourseFontSize = 18
	scheduleGridLargeMetaFontSize   = 13
	scheduleGridBadgeFontSize       = 10
	scheduleGridBadgeHeight         = 24
	scheduleGridBadgePaddingX       = 6
	scheduleGridBadgeRadius         = 4
)

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

func scheduleGridItemUsesLargeText(rect image.Rectangle, metrics scheduleGridMetrics) bool {
	return rect.Dy() >= 2*metrics.RowHeight
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

func normalizeScheduleGridCourseKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func (r Renderer) renderScheduleGridPNG(title string, grid *ScheduleGrid) ([]byte, int, int, error) {
	metrics := defaultScheduleGridMetrics(len(grid.Days), len(grid.Periods))
	space := metrics.space()
	s := space.px
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	todayIndex := scheduleGridTodayIndex(grid, now)
	faces, err := r.richFaces(space.Scale)
	if err != nil {
		return nil, 0, 0, err
	}
	itemCourseFace, err := r.sansBoldFontFace(float64(scheduleGridCourseFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	itemCourseMonoFace, err := r.monoBoldFontFace(float64(scheduleGridCourseFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	itemMetaFace, err := r.sansFontFace(float64(scheduleGridMetaFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	itemMetaMonoFace, err := r.monoFontFace(float64(scheduleGridMetaFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	largeCourseFace, err := r.sansBoldFontFace(float64(scheduleGridLargeCourseFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	largeCourseMonoFace, err := r.monoBoldFontFace(float64(scheduleGridLargeCourseFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	largeMetaFace, err := r.sansFontFace(float64(scheduleGridLargeMetaFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	largeMetaMonoFace, err := r.monoFontFace(float64(scheduleGridLargeMetaFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	badgeFace, err := r.sansBoldFontFace(float64(scheduleGridBadgeFontSize * space.Scale))
	if err != nil {
		return nil, 0, 0, err
	}
	badgeMonoFace, err := r.monoBoldFontFace(float64(scheduleGridBadgeFontSize * space.Scale))
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
	} else if scheduleGridHasNoDates(grid) {
		summary = "整学期 · 第 1–" + strconv.Itoa(len(grid.Periods)) + " 节"
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
		headerBaseline := rect.Min.Y + 23
		if strings.TrimSpace(day.Date) == "" {
			headerBaseline = rect.Min.Y + 34
		}
		drawCenteredMixedText(canvas, faces.Bold, faces.BoldMono, s(rect.Min.X+metrics.DayWidth/2), s(headerBaseline), headerText, headerColor)
		if strings.TrimSpace(day.Date) != "" {
			drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(rect.Min.X+metrics.DayWidth/2), s(rect.Min.Y+43), day.Date, dateColor)
		}
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
		badgeRight := rect.Max.X - 8
		for _, label := range scheduleGridRoleLabels(item) {
			badgeWidth := richTextWidth(label, scheduleGridBadgeFontSize) + 2*scheduleGridBadgePaddingX
			badge := image.Rect(
				badgeRight-badgeWidth,
				rect.Min.Y+8,
				badgeRight,
				rect.Min.Y+8+scheduleGridBadgeHeight,
			)
			drawScheduleGridBadge(canvas, badge, s, badgeFace, badgeMonoFace, label)
			badgeRight -= badgeWidth + 4
		}

		centerX := s(rect.Min.X + rect.Dx()/2)
		centerY := s(rect.Min.Y + rect.Dy()/2)
		courseFace, courseMonoFace := itemCourseFace, itemCourseMonoFace
		metaFace, metaMonoFace := itemMetaFace, itemMetaMonoFace
		courseFontSize, metaFontSize := scheduleGridCourseFontSize, scheduleGridMetaFontSize
		maxTextWidth := metrics.DayWidth - 12
		oneLineOffset := 6
		twoCourseOffset, twoMetaOffset := -4, 18
		threeCourseOffset, threeLocationOffset, threeWeeksOffset := -15, 3, 21
		if scheduleGridItemUsesLargeText(rect, metrics) {
			if richTextWidth(strings.TrimSpace(item.Course), scheduleGridLargeCourseFontSize) <= maxTextWidth {
				courseFace, courseMonoFace = largeCourseFace, largeCourseMonoFace
				courseFontSize = scheduleGridLargeCourseFontSize
			}
			if richTextWidth(strings.TrimSpace(item.Location), scheduleGridLargeMetaFontSize) <= maxTextWidth &&
				richTextWidth(strings.TrimSpace(item.Weeks), scheduleGridLargeMetaFontSize) <= maxTextWidth {
				metaFace, metaMonoFace = largeMetaFace, largeMetaMonoFace
				metaFontSize = scheduleGridLargeMetaFontSize
			}
			oneLineOffset = 7
			twoCourseOffset, twoMetaOffset = -7, 23
			threeCourseOffset, threeLocationOffset, threeWeeksOffset = -22, 4, 31
		}
		course := fitScheduleGridText(item.Course, maxTextWidth, courseFontSize)
		location := fitScheduleGridText(item.Location, maxTextWidth, metaFontSize)
		weeks := fitScheduleGridText(item.Weeks, maxTextWidth, metaFontSize)
		switch {
		case location == "" && weeks == "":
			drawCenteredMixedText(canvas, courseFace, courseMonoFace, centerX, centerY+s(oneLineOffset), course, ink)
		case weeks == "":
			drawCenteredMixedText(canvas, courseFace, courseMonoFace, centerX, centerY+s(twoCourseOffset), course, ink)
			drawCenteredMixedText(canvas, metaFace, metaMonoFace, centerX, centerY+s(twoMetaOffset), location, muted)
		case location == "":
			drawCenteredMixedText(canvas, courseFace, courseMonoFace, centerX, centerY+s(twoCourseOffset), course, ink)
			drawCenteredMixedText(canvas, metaFace, metaMonoFace, centerX, centerY+s(twoMetaOffset), weeks, accent)
		default:
			drawCenteredMixedText(canvas, courseFace, courseMonoFace, centerX, centerY+s(threeCourseOffset), course, ink)
			drawCenteredMixedText(canvas, metaFace, metaMonoFace, centerX, centerY+s(threeLocationOffset), location, muted)
			drawCenteredMixedText(canvas, metaFace, metaMonoFace, centerX, centerY+s(threeWeeksOffset), weeks, accent)
		}
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

func scheduleGridRoleLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "teaching_assistant":
		return "助教"
	case "auditor":
		return "旁听"
	default:
		return ""
	}
}

func scheduleGridRoleLabels(item ScheduleGridItem) []string {
	kinds := append([]string{item.Kind}, item.AdditionalKinds...)
	labels := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		label := scheduleGridRoleLabel(kind)
		if label == "" || containsScheduleGridLabel(labels, label) {
			continue
		}
		labels = append(labels, label)
	}
	return labels
}

func containsScheduleGridLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

func scheduleGridHasNoDates(grid *ScheduleGrid) bool {
	if grid == nil || len(grid.Days) == 0 {
		return false
	}
	for _, day := range grid.Days {
		if strings.TrimSpace(day.Date) != "" {
			return false
		}
	}
	return true
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

func drawScheduleGridBadge(dst *image.RGBA, rect image.Rectangle, scale func(int) int, textFace, monoFace font.Face, label string) {
	scaled := image.Rect(scale(rect.Min.X), scale(rect.Min.Y), scale(rect.Max.X), scale(rect.Max.Y))
	drawRoundedRect(dst, scaled, scale(scheduleGridBadgeRadius), color.RGBA{239, 68, 68, 255})
	drawCenteredMixedText(
		dst,
		textFace,
		monoFace,
		scaled.Min.X+scaled.Dx()/2,
		scaled.Min.Y+scale(17),
		label,
		color.RGBA{255, 255, 255, 255},
	)
}

func drawRoundedRect(dst *image.RGBA, rect image.Rectangle, radius int, fill color.Color) {
	if rect.Empty() {
		return
	}
	if radius <= 0 {
		drawRect(dst, rect, fill)
		return
	}
	maxRadius := rect.Dx() / 2
	if heightRadius := rect.Dy() / 2; heightRadius < maxRadius {
		maxRadius = heightRadius
	}
	if radius > maxRadius {
		radius = maxRadius
	}
	radiusSquared := radius * radius
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			cornerX := x
			if x < rect.Min.X+radius {
				cornerX = rect.Min.X + radius
			} else if x >= rect.Max.X-radius {
				cornerX = rect.Max.X - radius - 1
			}
			cornerY := y
			if y < rect.Min.Y+radius {
				cornerY = rect.Min.Y + radius
			} else if y >= rect.Max.Y-radius {
				cornerY = rect.Max.Y - radius - 1
			}
			dx, dy := x-cornerX, y-cornerY
			if dx*dx+dy*dy <= radiusSquared {
				dst.Set(x, y, fill)
			}
		}
	}
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
