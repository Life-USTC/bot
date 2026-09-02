package responses

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/life_ustc_logo_raw.png
var embeddedBusLogoPNG []byte

type Renderer struct {
	FontPath string
}

type responseCardTheme struct {
	Label      string
	Background color.RGBA
	Header     color.RGBA
	Accent     color.RGBA
	Border     color.RGBA
	Shadow     color.RGBA
	Title      color.RGBA
	Body       color.RGBA
}

var defaultFontPaths = []string{
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Regular.otf",
	"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-fonts/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
}

var busSansFontPaths = []string{
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Regular.otf",
	"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-fonts/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
}

var busSansBoldFontPaths = []string{
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Bold.otf",
	"/usr/share/fonts/google-noto-sans-cjk-fonts/NotoSansCJK-Bold.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
}

var busMonoFontPaths = []string{
	"/usr/share/fonts/truetype/firacode/FiraCode-Regular.ttf",
	"/usr/share/fonts/fira-code/FiraCode-Regular.ttf",
	"/tmp/FiraCode/ttf/FiraCode-Regular.ttf",
	"/tmp/FiraCode/ttf/FiraCode-Medium.ttf",
	"/usr/share/fonts/google-noto-vf-fonts/NotoSansMono[wght].ttf",
	"/usr/share/fonts/google-noto-sans-mono-cjk-vf-fonts/NotoSansMonoCJK-VF.ttc",
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
}

var busMonoBoldFontPaths = []string{
	"/usr/share/fonts/truetype/firacode/FiraCode-Bold.ttf",
	"/usr/share/fonts/fira-code/FiraCode-Bold.ttf",
	"/tmp/FiraCode/ttf/FiraCode-Bold.ttf",
	"/usr/share/fonts/google-noto-vf-fonts/NotoSansMono[wght].ttf",
	"/usr/share/fonts/google-noto-sans-mono-cjk-vf-fonts/NotoSansMonoCJK-VF.ttc",
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf",
}

func (r Renderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	if img.Grid != nil {
		return r.renderScheduleGridPNG(img.Title, img.Grid)
	}
	if img.Weather != nil {
		return r.renderWeatherCardPNG(img.Weather)
	}
	if strings.TrimSpace(img.RichText) == "" {
		return nil, 0, 0, errors.New("response rich text is empty")
	}
	return r.renderRichPNG(img.RichText)
}

type busRenderTable struct {
	Header         []string
	HeaderEmphasis []bool
	Rows           []busRenderRow
}

func (t busRenderTable) endpoints() (start, end string) {
	if len(t.Header) == 0 {
		return "", ""
	}
	return strings.TrimSpace(t.Header[0]), strings.TrimSpace(t.Header[len(t.Header)-1])
}

func (t busRenderTable) endpointsKey() string {
	start, end := t.endpoints()
	if start == "" || end == "" || start == end {
		return start + end
	}
	if start < end {
		return start + "→" + end
	}
	return end + "→" + start
}

func (t busRenderTable) directionKey() string {
	parts := make([]string, 0, len(t.Header))
	for _, h := range t.Header {
		h = strings.TrimSpace(h)
		if h != "" {
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, "→")
}

type busRenderRow struct {
	Cells     []string
	Highlight bool
	Departed  bool
}

type busStopHeader struct {
	Text      string
	Emphasize bool
}

type imageRenderSpace struct {
	Width  int
	Height int
	Scale  int
}

func (s imageRenderSpace) px(value int) int {
	return value * s.Scale
}

func (s imageRenderSpace) bounds() image.Rectangle {
	return image.Rect(0, 0, s.px(s.Width), s.px(s.Height))
}

type busRenderMetrics struct {
	CanvasWidth       int
	Scale             int
	LeftMargin        int
	RightMargin       int
	TableColumnGap    int
	TableRowGap       int
	TableHeaderHeight int
	TableRowHeight    int
	ColumnWidth       int
	RouteTableTop     int
	AllRoutesTableTop int
	DirectionLabelGap int
	MarginBelowTable  int
	FooterLineGap     int
	MarginBottom      int
}

func defaultBusRenderMetrics() busRenderMetrics {
	return busRenderMetrics{
		CanvasWidth:       920,
		Scale:             2,
		LeftMargin:        52,
		RightMargin:       52,
		TableColumnGap:    20,
		TableRowGap:       30,
		TableHeaderHeight: 34,
		TableRowHeight:    42,
		ColumnWidth:       88,
		RouteTableTop:     72,
		AllRoutesTableTop: 84,
		DirectionLabelGap: 13,
		MarginBelowTable:  18,
		FooterLineGap:     14,
		MarginBottom:      12,
	}
}

type busTableLayout struct {
	Bounds         image.Rectangle
	ColumnWidth    int
	Headers        []busStopHeader
	DirectionLabel string
}

type busRenderLayout struct {
	Space          imageRenderSpace
	Metrics        busRenderMetrics
	Tables         []busTableLayout
	TableTop       int
	HeaderLines    []string
	FooterLines    []string
	NextTime       string
	NextWait       string
	TableBottom    int
	FooterY        int
	LogoOpacity    float64
	LogoCenterX    int
	LogoCenterY    int
	LogoSize       int
	UsesSerifFont  bool
	VerticalLayout bool
}

func busRenderTitle(img *Image) string {
	title := strings.TrimSpace(img.Title)
	title = strings.TrimPrefix(title, "校车 ")
	if title == "校车" || title == "" {
		return "校车"
	}
	return title
}

func busDayType(now time.Time) string {
	switch now.Weekday() {
	case time.Saturday, time.Sunday:
		return "周末"
	default:
		return "工作日"
	}
}

func busNextWait(tables []busRenderTable, title string, now time.Time) (string, string) {
	now = now.In(time.FixedZone("CST", 8*60*60))
	nowMinutes := now.Hour()*60 + now.Minute()
	_, dest := parseBusEndpoints(title)
	best := -1
	for _, table := range tables {
		destCol := -1
		for i, h := range table.Header {
			if h == dest && dest != "" {
				destCol = i
				break
			}
		}
		for _, row := range table.Rows {
			cell := ""
			if destCol >= 0 && destCol < len(row.Cells) {
				cell = row.Cells[destCol]
			} else if len(row.Cells) > 0 {
				cell = row.Cells[0]
			}
			if cell == "" {
				continue
			}
			minutes, ok := parseBusClock(cell)
			if !ok || minutes < nowMinutes {
				continue
			}
			if best < 0 || minutes < best {
				best = minutes
			}
		}
	}
	if best < 0 {
		return "", ""
	}
	return formatBusClock(best), formatBusWait(best - nowMinutes)
}

func formatBusClock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func formatBusWait(minutes int) string {
	if minutes <= 0 {
		return "现在"
	}
	return strconv.Itoa(minutes) + " 分钟"
}

func parseBusClock(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
		return 0, false
	}
	hour, ok := parseTwoDigitNumber(parts[0])
	if !ok {
		return 0, false
	}
	minute, ok := parseTwoDigitNumber(parts[1])
	if !ok || hour > 23 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func parseTwoDigitNumber(value string) (int, bool) {
	if len(value) > 2 {
		return 0, false
	}
	out := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		out = out*10 + int(r-'0')
	}
	return out, true
}

func markBusRowsByTime(tables []busRenderTable, title string, now time.Time) {
	now = now.In(time.FixedZone("CST", 8*60*60))
	nowMinutes := now.Hour()*60 + now.Minute()
	_, dest := parseBusEndpoints(title)
	bestRoute := -1
	bestRow := -1
	bestArrival := -1
	for ti, table := range tables {
		destCol := -1
		for i, h := range table.Header {
			if h == dest && dest != "" {
				destCol = i
				break
			}
		}
		for ri := range table.Rows {
			if len(table.Rows[ri].Cells) == 0 {
				continue
			}
			minutes, ok := parseBusClock(table.Rows[ri].Cells[0])
			if !ok {
				continue
			}
			if minutes < nowMinutes {
				table.Rows[ri].Departed = true
				continue
			}
			arrival := minutes
			if destCol >= 0 && destCol < len(table.Rows[ri].Cells) {
				if arrivalMin, ok := parseBusClock(table.Rows[ri].Cells[destCol]); ok {
					arrival = arrivalMin
				}
			}
			if bestArrival < 0 || arrival < bestArrival {
				bestArrival = arrival
				bestRoute = ti
				bestRow = ri
			}
		}
	}
	if bestRoute >= 0 && bestRow >= 0 {
		tables[bestRoute].Rows[bestRow].Highlight = true
	}
}

func busRenderTables(img *Image) []busRenderTable {
	if img == nil {
		return nil
	}
	tables := []busRenderTable{}
	block := []string{}
	flush := func() {
		if len(block) == 0 {
			return
		}
		table := busRenderTable{Header: splitBusTableCells(block[0])}
		for _, line := range block[1:] {
			cells := splitBusTableCells(line)
			if len(cells) == 0 {
				continue
			}
			row := busRenderRow{}
			if cells[len(cells)-1] == "✨" {
				row.Highlight = true
				cells = cells[:len(cells)-1]
			}
			if len(cells) > len(table.Header) {
				cells = cells[:len(table.Header)]
			}
			row.Cells = cells
			table.Rows = append(table.Rows, row)
		}
		if len(table.Header) > 0 && len(table.Rows) > 0 {
			tables = append(tables, table)
		}
		block = nil
	}
	for _, line := range img.Lines {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		block = append(block, line)
	}
	flush()
	return tables
}

func splitBusTableCells(line string) []string {
	fields := strings.Fields(line)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

// busTablePairRows groups tables by their unordered endpoints. Within each group,
// tables whose headers are exact reverses of each other are paired and placed on
// the same row when they fit. A group never shares a row with another group, so
// unrelated routes are not displayed side by side.
func busTablePairRows(tables []busRenderTable, availableWidth, columnWidth, gap int) [][]busRenderTable {
	if len(tables) == 0 {
		return nil
	}
	if len(tables) == 1 {
		return [][]busRenderTable{{tables[0]}}
	}

	groups := make(map[string][]int)
	order := []string{}
	for i, t := range tables {
		key := t.endpointsKey()
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}

	// Groups that contain an exact reverse pair are laid out first.
	sort.SliceStable(order, func(i, j int) bool {
		return hasReversePair(groups[order[i]], tables) && !hasReversePair(groups[order[j]], tables)
	})

	var rows [][]busRenderTable
	for _, key := range order {
		rows = append(rows, pairRowsForGroup(groups[key], tables, availableWidth, columnWidth, gap)...)
	}
	return rows
}

func hasReversePair(indices []int, tables []busRenderTable) bool {
	for i, idxI := range indices {
		for j := i + 1; j < len(indices); j++ {
			if isReverseRoute(tables[idxI].Header, tables[indices[j]].Header) {
				return true
			}
		}
	}
	return false
}

func pairRowsForGroup(indices []int, tables []busRenderTable, availableWidth, columnWidth, gap int) [][]busRenderTable {
	if len(indices) == 1 {
		return [][]busRenderTable{{tables[indices[0]]}}
	}
	paired := make(map[int]bool)
	var rows [][]busRenderTable
	for i, idxI := range indices {
		if paired[idxI] {
			continue
		}
		tI := tables[idxI]
		pairIdx := -1
		for j := i + 1; j < len(indices); j++ {
			idxJ := indices[j]
			if paired[idxJ] {
				continue
			}
			if isReverseRoute(tI.Header, tables[idxJ].Header) {
				pairIdx = idxJ
				break
			}
		}
		if pairIdx >= 0 {
			paired[pairIdx] = true
			tJ := tables[pairIdx]
			pairWidth := len(tI.Header)*columnWidth + gap + len(tJ.Header)*columnWidth
			if pairWidth <= availableWidth {
				rows = append(rows, []busRenderTable{tI, tJ})
			} else {
				rows = append(rows, []busRenderTable{tI})
				rows = append(rows, []busRenderTable{tJ})
			}
		} else {
			rows = append(rows, []busRenderTable{tI})
		}
	}
	return rows
}

func isReverseRoute(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := 0; i < len(a); i++ {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[len(b)-1-i]) {
			return false
		}
	}
	return true
}

func busTableHeight(table busRenderTable, headerH, rowH int) int {
	return headerH + len(table.Rows)*rowH
}

func drawBusTable(dst *image.RGBA, renderedHeaders []busStopHeader, table busRenderTable, x, y, width int, columnWidths []int, headerH, rowH, cellPadding, scale int, headerFace, headerMonoFace, headerEmphasisFace, headerEmphasisMonoFace, bodyFace, monoFace font.Face, headerBg, rowBg, highlightBg, line, ink, departed, highlightText color.RGBA) {
	height := busTableHeight(table, headerH, rowH)
	cols := min(len(table.Header), len(columnWidths))

	drawRect(dst, image.Rect(x, y, x+width, y+height), rowBg)
	drawRect(dst, image.Rect(x, y, x+width, y+headerH), headerBg)
	for ri, row := range table.Rows {
		rowY := y + headerH + ri*rowH
		if row.Highlight {
			drawRect(dst, image.Rect(x, rowY, x+width, rowY+rowH), highlightBg)
		}
	}

	for ri := 0; ri < len(table.Rows); ri++ {
		lineY := y + headerH + ri*rowH
		drawRect(dst, image.Rect(x, lineY, x+width, lineY+scale), line)
	}

	cellX := x
	for i, header := range renderedHeaders {
		if i >= cols {
			break
		}
		face := headerFace
		monoFace := headerMonoFace
		if header.Emphasize {
			face = headerEmphasisFace
			monoFace = headerEmphasisMonoFace
		}
		drawCenteredMixedText(dst, face, monoFace, cellX+columnWidths[i]/2, y+headerH/2+5*scale, fitRichTextToWidth(header.Text, (columnWidths[i]-2*cellPadding)/scale, 13), ink)
		cellX += columnWidths[i]
	}
	for ri, row := range table.Rows {
		rowY := y + headerH + ri*rowH
		textColor := ink
		if row.Highlight {
			textColor = highlightText
		} else if row.Departed {
			textColor = departed
		}
		cellX := x
		for ci := 0; ci < cols; ci++ {
			cell := ""
			if ci < len(row.Cells) {
				cell = row.Cells[ci]
			}
			cell = fitRichTextToWidth(cell, (columnWidths[ci]-2*cellPadding)/scale, 14)
			drawMixedText(dst, bodyFace, monoFace, cellX+cellPadding, rowY+rowH/2+5*scale, cell, textColor)
			cellX += columnWidths[ci]
		}
	}
}

func drawBusLogoWatermark(dst *image.RGBA, bounds image.Rectangle, size int, opacity float64) {
	logo := loadBusLogo()
	if logo == nil {
		return
	}
	// Place the logo mostly outside the canvas so only the top-left quadrant peeks
	// into the bottom-right corner. This keeps it visible as a watermark without
	// overlapping the footer text or table content.
	centerX := bounds.Max.X + size/4
	centerY := bounds.Max.Y + size/4
	drawRotatedLogoTile(dst, logo, centerX, centerY, size, opacity, -math.Pi/6)
}

func drawRotatedLogoTile(dst *image.RGBA, src image.Image, centerX, centerY, size int, opacity float64, angle float64) {
	bounds := src.Bounds()
	if bounds.Empty() || size <= 0 || opacity <= 0 {
		return
	}
	cosA := math.Cos(angle)
	sinA := math.Sin(angle)
	half := float64(size) / 2
	for y := centerY - size/2; y <= centerY+size/2; y++ {
		for x := centerX - size/2; x <= centerX+size/2; x++ {
			if !image.Pt(x, y).In(dst.Bounds()) {
				continue
			}
			dx := float64(x - centerX)
			dy := float64(y - centerY)
			u := dx*cosA - dy*sinA + half
			v := dx*sinA + dy*cosA + half
			if u < 0 || v < 0 || u >= float64(size) || v >= float64(size) {
				continue
			}
			sx := bounds.Min.X + int(u*float64(bounds.Dx())/float64(size))
			sy := bounds.Min.Y + int(v*float64(bounds.Dy())/float64(size))
			blendPixel(dst, x, y, src.At(sx, sy), opacity)
		}
	}
}

func (r Renderer) sansBoldFontFace(size float64) (font.Face, error) {
	face, err := r.loadFont(busSansBoldFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.sansFontFace(size)
}

func (r Renderer) sansFontFace(size float64) (font.Face, error) {
	face, err := r.loadFont(busSansFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.loadFont(defaultFontPaths, size)
}

func (r Renderer) monoFontFace(size float64) (font.Face, error) {
	face, err := loadFirstFont(busMonoFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.sansFontFace(size)
}

func (r Renderer) monoBoldFontFace(size float64) (font.Face, error) {
	face, err := loadFirstFont(busMonoBoldFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.monoFontFace(size)
}

func (r Renderer) loadFont(candidates []string, size float64) (font.Face, error) {
	path := strings.TrimSpace(r.FontPath)
	if path == "" {
		return loadFirstFont(candidates, size)
	}
	return loadFontPath(path, size)
}

func loadFirstFont(candidates []string, size float64) (font.Face, error) {
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return loadFontPath(candidate, size)
		}
	}
	return nil, errors.New("no font path configured")
}

func loadFontPath(path string, size float64) (font.Face, error) {
	if path == "" {
		return nil, errors.New("no font path configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	collection, err := opentype.ParseCollection(data)
	if err != nil {
		return nil, err
	}
	if collection.NumFonts() == 0 {
		return nil, errors.New("font collection is empty")
	}
	parsed, err := collection.Font(0)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func busStopHeaders(headers []string, addPrefix bool) []busStopHeader {
	if len(headers) == 0 {
		return nil
	}
	from, to := "", ""
	if len(headers) > 0 {
		from = strings.TrimSpace(headers[0])
	}
	if len(headers) > 1 {
		to = strings.TrimSpace(headers[len(headers)-1])
	}
	out := make([]busStopHeader, len(headers))
	for i, h := range headers {
		h = strings.TrimSpace(h)
		out[i].Text = h
		if i == 0 && h == from && h != "" {
			if addPrefix {
				out[i].Text = "出发·" + h
			}
			out[i].Emphasize = true
		}
		if i == len(headers)-1 && h == to && h != "" && h != from {
			if addPrefix {
				out[i].Text = "到·" + h
			}
			out[i].Emphasize = true
		}
	}
	return out
}

func parseBusEndpoints(title string) (string, string) {
	title = strings.TrimSpace(title)
	prefixes := []string{"校车 · ", "校车 ", "校车", "到 "}
	for _, p := range prefixes {
		if strings.HasPrefix(title, p) {
			title = strings.TrimPrefix(title, p)
			break
		}
	}
	if title == "校车" || title == "" {
		return "", ""
	}
	for _, sep := range []string{"→", "-", "到"} {
		if parts := strings.SplitN(title, sep, 2); len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return title, ""
}

func busRenderLayoutFor(img *Image, now time.Time) busRenderLayout {
	tables := busRenderTables(img)
	metrics := defaultBusRenderMetrics()
	availableWidth := metrics.CanvasWidth - metrics.LeftMargin - metrics.RightMargin

	showDirectionLabels := busRenderTitle(img) == "校车"
	tableTop := metrics.RouteTableTop
	if showDirectionLabels {
		tableTop = metrics.AllRoutesTableTop
	}

	rows := busTablePairRows(tables, availableWidth, metrics.ColumnWidth, metrics.TableColumnGap)
	verticalLayout := len(rows) > 1 || (len(rows) == 1 && len(rows[0]) > 1)

	tableLayouts := make([]busTableLayout, 0, len(tables))
	tableBottom := tableTop
	y := tableTop
	for _, row := range rows {
		x := metrics.LeftMargin
		rowBottom := y
		for _, table := range row {
			tableW := len(table.Header) * metrics.ColumnWidth
			if tableW > availableWidth {
				tableW = availableWidth
			}
			columnWidth := metrics.ColumnWidth
			if len(row) == 1 && len(table.Header)*columnWidth > availableWidth {
				columnWidth = tableW / max(1, len(table.Header))
			}
			bottom := y + metrics.TableHeaderHeight + len(table.Rows)*metrics.TableRowHeight
			directionLabel := ""
			if showDirectionLabels {
				directionLabel = table.directionKey()
			}
			tableLayouts = append(tableLayouts, busTableLayout{
				Bounds:         image.Rect(x, y, x+tableW, bottom),
				ColumnWidth:    columnWidth,
				Headers:        busStopHeaders(table.Header, true),
				DirectionLabel: directionLabel,
			})
			if bottom > rowBottom {
				rowBottom = bottom
			}
			if bottom > tableBottom {
				tableBottom = bottom
			}
			x += tableW + metrics.TableColumnGap
		}
		y = rowBottom + metrics.TableRowGap
	}

	footerY := tableBottom + metrics.MarginBelowTable
	height := footerY + metrics.FooterLineGap + metrics.MarginBottom
	nextTime, nextWait := busNextWait(tables, "校车 · "+busRenderTitle(img), now)
	title := busRenderTitle(img)
	if title == "校车" {
		title = "全部路线"
	}
	layout := busRenderLayout{
		Space: imageRenderSpace{
			Width:  metrics.CanvasWidth,
			Height: height,
			Scale:  metrics.Scale,
		},
		Metrics:  metrics,
		Tables:   tableLayouts,
		TableTop: tableTop,
		HeaderLines: []string{
			"Life @ USTC",
			"校车 · " + title,
		},
		FooterLines: []string{
			now.Format("2006-01-02 15:04") + "（" + busDayType(now) + "）",
			"2026 春季学期时刻表 / 蜗壳小道消息",
		},
		NextTime:       nextTime,
		NextWait:       nextWait,
		TableBottom:    tableBottom,
		FooterY:        footerY,
		LogoOpacity:    0.15,
		LogoCenterX:    0,
		LogoCenterY:    0,
		LogoSize:       120,
		UsesSerifFont:  true,
		VerticalLayout: verticalLayout,
	}
	return layout
}

func DefaultFontPathsForTest() []string {
	return append([]string(nil), defaultFontPaths...)
}

func cardTheme(kind string) responseCardTheme {
	theme := responseCardTheme{
		Label:      "Life @ USTC",
		Background: color.RGBA{241, 245, 249, 255},
		Header:     color.RGBA{248, 250, 252, 255},
		Accent:     color.RGBA{37, 99, 235, 255},
		Border:     color.RGBA{203, 213, 225, 255},
		Shadow:     color.RGBA{226, 232, 240, 255},
		Title:      color.RGBA{15, 23, 42, 255},
		Body:       color.RGBA{30, 41, 59, 255},
	}
	switch strings.TrimSpace(kind) {
	case "bus":
		theme.Label = "校车"
		theme.Background = color.RGBA{240, 253, 244, 255}
		theme.Header = color.RGBA{236, 253, 245, 255}
		theme.Accent = color.RGBA{22, 163, 74, 255}
	case "todo":
		theme.Label = "待办"
		theme.Background = color.RGBA{255, 251, 235, 255}
		theme.Header = color.RGBA{254, 243, 199, 255}
		theme.Accent = color.RGBA{217, 119, 6, 255}
	case "overview":
		theme.Label = "今日"
		theme.Background = color.RGBA{245, 243, 255, 255}
		theme.Header = color.RGBA{237, 233, 254, 255}
		theme.Accent = color.RGBA{124, 58, 237, 255}
	case "dashboard":
		theme.Label = "概览"
		theme.Background = color.RGBA{240, 249, 255, 255}
		theme.Header = color.RGBA{224, 242, 254, 255}
		theme.Accent = color.RGBA{2, 132, 199, 255}
	case "weather":
		theme.Label = "天气"
		theme.Background = color.RGBA{240, 249, 255, 255}
		theme.Header = color.RGBA{224, 242, 254, 255}
		theme.Accent = color.RGBA{217, 119, 6, 255}
	case "deadlines":
		theme.Label = "截止"
		theme.Background = color.RGBA{255, 247, 237, 255}
		theme.Header = color.RGBA{255, 237, 213, 255}
		theme.Accent = color.RGBA{234, 88, 12, 255}
	case "schedule":
		theme.Label = "课表"
	case "homework":
		theme.Label = "作业"
	case "exam":
		theme.Label = "考试"
	case "nextclass":
		theme.Label = "下一节"
	case "class_reminder":
		theme.Label = "课前提醒"
	case "homework_reminder":
		theme.Label = "作业提醒"
	}
	return theme
}

func drawRect(dst *image.RGBA, rect image.Rectangle, c color.Color) {
	draw.Draw(dst, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

type mixedFontRun struct {
	Text string
	Face font.Face
}

func drawMixedText(dst *image.RGBA, textFace, monoFace font.Face, x, y int, text string, c color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Dot: fixed.P(x, y)}
	for _, run := range mixedFontRuns(text, textFace, monoFace) {
		d.Face = run.Face
		d.DrawString(run.Text)
	}
}

func drawCenteredMixedText(dst *image.RGBA, textFace, monoFace font.Face, center, y int, text string, c color.Color) {
	runs := mixedFontRuns(text, textFace, monoFace)
	dot := fixed.P(center, y)
	dot.X -= mixedTextAdvance(runs) / 2
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Dot: dot}
	for _, run := range runs {
		d.Face = run.Face
		d.DrawString(run.Text)
	}
}

func drawRightMixedText(dst *image.RGBA, textFace, monoFace font.Face, right, y int, text string, c color.Color) {
	runs := mixedFontRuns(text, textFace, monoFace)
	dot := fixed.P(right, y)
	dot.X -= mixedTextAdvance(runs)
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Dot: dot}
	for _, run := range runs {
		d.Face = run.Face
		d.DrawString(run.Text)
	}
}

func mixedTextAdvance(runs []mixedFontRun) fixed.Int26_6 {
	advance := fixed.Int26_6(0)
	for _, run := range runs {
		advance += font.MeasureString(run.Face, run.Text)
	}
	return advance
}

func mixedFontRuns(text string, textFace, monoFace font.Face) []mixedFontRun {
	runs := []mixedFontRun{}
	var current strings.Builder
	currentMono := false
	hasCurrent := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		face := textFace
		if currentMono {
			face = monoFace
		}
		runs = append(runs, mixedFontRun{Text: current.String(), Face: face})
		current.Reset()
	}
	appendRune := func(r rune) {
		mono := usesMonoFont(r)
		face, fallback := textFace, monoFace
		if mono {
			face, fallback = monoFace, textFace
		}
		if !fontHasGlyph(face, r) {
			if !fontHasGlyph(fallback, r) {
				return
			}
			mono = !mono
		}
		if hasCurrent && mono != currentMono {
			flush()
		}
		currentMono = mono
		hasCurrent = true
		current.WriteRune(r)
	}
	for _, r := range text {
		switch r {
		case '\t':
			appendRune(' ')
			appendRune(' ')
		case '\r':
			continue
		case '\n':
			appendRune(' ')
		default:
			if r < ' ' {
				r = ' '
			}
			appendRune(r)
		}
	}
	flush()
	return runs
}

func usesMonoFont(r rune) bool {
	if r >= ' ' && r <= unicode.MaxASCII {
		return true
	}
	return unicode.IsDigit(r) || unicode.Is(unicode.Latin, r)
}

func fontHasGlyph(face font.Face, r rune) bool {
	if face == nil {
		return false
	}
	_, ok := face.GlyphAdvance(r)
	return ok
}

func loadBusLogo() image.Image {
	for _, path := range []string{
		"/home/tiankaima/Source/Life-USTC/server/public/images/icon.png",
		"../server/public/images/icon.png",
		"../../server/public/images/icon.png",
		"assets/life_ustc_logo_raw.png",
	} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		img, _, err := image.Decode(file)
		_ = file.Close()
		if err == nil {
			return img
		}
	}
	return loadEmbeddedBusLogo()
}

func loadEmbeddedBusLogo() image.Image {
	img, _, err := image.Decode(bytes.NewReader(embeddedBusLogoPNG))
	if err != nil {
		return nil
	}
	return img
}

func blendPixel(dst *image.RGBA, x, y int, src color.Color, opacity float64) {
	if !image.Pt(x, y).In(dst.Bounds()) {
		return
	}
	sr, sg, sb, sa := src.RGBA()
	alpha := float64(sa) / 65535 * opacity
	if alpha <= 0 {
		return
	}
	dr, dg, db, da := dst.At(x, y).RGBA()
	inv := 1 - alpha
	dst.SetRGBA(x, y, color.RGBA{
		R: uint8((float64(sr>>8)*alpha + float64(dr>>8)*inv) + 0.5),
		G: uint8((float64(sg>>8)*alpha + float64(dg>>8)*inv) + 0.5),
		B: uint8((float64(sb>>8)*alpha + float64(db>>8)*inv) + 0.5),
		A: uint8((float64(sa>>8)*alpha + float64(da>>8)*inv) + 0.5),
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
