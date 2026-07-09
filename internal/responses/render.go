package responses

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

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
	"/home/tiankaima/.local/share/fonts/source-han-serif-sc/SourceHanSerifSC-Regular.otf",
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Regular.otf",
}

var busSansBoldFontPaths = []string{
	"/home/tiankaima/.local/share/fonts/source-han-serif-sc/SourceHanSerifSC-Bold.otf",
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Bold.otf",
}

var busMonoFontPaths = []string{
	"/tmp/FiraCode/ttf/FiraCode-Regular.ttf",
	"/tmp/FiraCode/ttf/FiraCode-Medium.ttf",
}

var busSerifFontPaths = []string{
	"/home/tiankaima/.local/share/fonts/source-han-serif-sc/SourceHanSerifSC-Regular.otf",
}

var busSerifBoldFontPaths = []string{
	"/home/tiankaima/.local/share/fonts/source-han-serif-sc/SourceHanSerifSC-Bold.otf",
}

func (r Renderer) serifFontFace(size float64) (font.Face, error) {
	face, err := r.loadFont(busSerifFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.sansFontFace(size)
}

func (r Renderer) serifBoldFontFace(size float64) (font.Face, error) {
	face, err := r.loadFont(busSerifBoldFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.sansBoldFontFace(size)
}

func (r Renderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	if strings.TrimSpace(img.Kind) == "bus" {
		return r.renderBusPNG(img)
	}
	face, err := r.fontFace(30)
	if err != nil {
		return nil, 0, 0, err
	}
	titleFace, err := r.fontFace(38)
	if err != nil {
		return nil, 0, 0, err
	}
	labelFace, err := r.fontFace(20)
	if err != nil {
		return nil, 0, 0, err
	}
	theme := cardTheme(img.Kind)
	lines := wrappedLines(img.Lines, 38)
	width := 920
	lineHeight := 40
	height := 156 + len(lines)*lineHeight + 42
	if height < 280 {
		height = 280
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	drawRect(canvas, canvas.Bounds(), theme.Background)
	card := image.Rect(30, 30, width-30, height-30)
	drawRect(canvas, image.Rect(card.Min.X+5, card.Min.Y+6, card.Max.X+5, card.Max.Y+6), theme.Shadow)
	drawRect(canvas, card, color.RGBA{255, 255, 255, 255})
	drawRect(canvas, image.Rect(card.Min.X, card.Min.Y, card.Max.X, card.Min.Y+96), theme.Header)
	drawRect(canvas, image.Rect(card.Min.X, card.Min.Y, card.Min.X+10, card.Max.Y), theme.Accent)
	drawRect(canvas, image.Rect(card.Min.X, card.Min.Y, card.Max.X, card.Min.Y+1), theme.Border)
	drawRect(canvas, image.Rect(card.Min.X, card.Max.Y-1, card.Max.X, card.Max.Y), theme.Border)
	drawRect(canvas, image.Rect(card.Min.X, card.Min.Y, card.Min.X+1, card.Max.Y), theme.Border)
	drawRect(canvas, image.Rect(card.Max.X-1, card.Min.Y, card.Max.X, card.Max.Y), theme.Border)
	drawRect(canvas, image.Rect(58, 126, width-58, 127), theme.Border)
	drawText(canvas, labelFace, 58, 65, theme.Label, theme.Accent)
	drawText(canvas, titleFace, 58, 106, img.Title, theme.Title)
	y := 166
	for _, line := range lines {
		drawText(canvas, face, 58, y, line, theme.Body)
		y += lineHeight
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), width, height, nil
}

type busRenderTable struct {
	Header []string
	Rows   []busRenderRow
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

type busRenderLayout struct {
	ColumnWidth    int
	TableWidths    []int
	TablePositions []image.Point
	TableTop       int
	HeaderLines    []string
	FooterLines    []string
	NextTime       string
	NextWait       string
	TableBottom    int
	FooterY        int
	Height         int
	LogoOpacity    float64
	LogoCenterX    int
	LogoCenterY    int
	LogoSize       int
	UsesSerifFont  bool
	TableHeaders   [][]busStopHeader
}

func (r Renderer) renderBusPNG(img *Image) ([]byte, int, int, error) {
	tables := busRenderTables(img)
	if len(tables) == 0 {
		return r.renderTextBusFallbackPNG(img)
	}
	now := time.Now().In(time.FixedZone("CST", 8*60*60))
	markBusRowsByTime(tables, "校车 · "+busRenderTitle(img), now)
	layout := busRenderLayoutFor(img, now)

	scale := 2
	s := func(n int) int { return n * scale }
	logicalWidth := 920
	logicalHeight := layout.Height
	width := s(logicalWidth)
	height := s(logicalHeight)

	sansFace, err := r.sansFontFace(float64(12 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	headFace, err := r.sansFontFace(float64(12 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	waitFace, err := r.sansFontFace(float64(12 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	tableHeadFace, err := r.sansFontFace(float64(18 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	tableHeadBoldFace, err := r.sansBoldFontFace(float64(18 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	monoFace, err := r.monoFontFace(float64(20 * scale))
	if err != nil {
		return nil, 0, 0, err
	}
	bodyFace, err := r.sansFontFace(float64(18 * scale))
	if err != nil {
		return nil, 0, 0, err
	}

	marginX := s(52)
	headerH := s(34)
	rowH := s(42)

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := color.RGBA{244, 248, 250, 255}
	ink := color.RGBA{15, 23, 42, 255}
	muted := color.RGBA{100, 116, 139, 255}
	line := color.RGBA{221, 229, 235, 255}
	rowBg := color.RGBA{255, 255, 255, 255}
	headBg := color.RGBA{248, 250, 252, 255}
	highlightBg := color.RGBA{224, 246, 239, 255}
	departed := color.RGBA{148, 163, 184, 255}
	accent := color.RGBA{15, 118, 110, 255}
	drawRect(canvas, canvas.Bounds(), bg)
	drawBusLogoWatermark(canvas, canvas.Bounds(), layout.LogoSize*scale, layout.LogoOpacity)
	drawText(canvas, headFace, marginX, s(32), layout.HeaderLines[0], muted)
	drawText(canvas, sansFace, marginX, s(56), layout.HeaderLines[1], ink)
	if layout.NextTime != "" {
		drawRightText(canvas, headFace, width-marginX, s(32), "下一班 "+layout.NextTime, muted)
		drawRightText(canvas, waitFace, width-marginX, s(56), layout.NextWait, accent)
	}

	for i, table := range tables {
		tableW := s(layout.TableWidths[i])
		position := layout.TablePositions[i]
		x := s(position.X)
		y := s(position.Y)
		renderedHeaders := busStopHeaders(table.Header, layout.HeaderLines[1])
		drawBusTable(canvas, renderedHeaders, table, x, y, tableW, s(layout.ColumnWidth), headerH, rowH, scale, tableHeadFace, tableHeadBoldFace, bodyFace, monoFace, headBg, rowBg, highlightBg, line, ink, muted, departed, accent)
	}

	footerY := s(layout.FooterY)
	drawRightText(canvas, headFace, width-marginX, footerY, layout.FooterLines[0], muted)
	drawRightText(canvas, headFace, width-marginX, footerY+s(18), layout.FooterLines[1], muted)

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), width, height, nil
}

func (r Renderer) renderTextBusFallbackPNG(img *Image) ([]byte, int, int, error) {
	clone := *img
	clone.Kind = "schedule"
	return r.RenderPNG(&clone)
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

func busCellIsNumeric(cell string) bool {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return false
	}
	if _, ok := parseBusClock(cell); ok {
		return true
	}
	for _, r := range cell {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
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

func maxBusTableColumns(tables []busRenderTable) int {
	maxColumns := 0
	for _, table := range tables {
		if len(table.Header) > maxColumns {
			maxColumns = len(table.Header)
		}
	}
	return maxColumns
}

func busTablesHeight(tables []busRenderTable, headerH, rowH int) int {
	if len(tables) == 0 {
		return 0
	}
	height := 0
	for _, table := range tables {
		height = max(height, busTableHeight(table, headerH, rowH))
	}
	return height
}

func busTableHeight(table busRenderTable, headerH, rowH int) int {
	return headerH + len(table.Rows)*rowH
}

func drawBusTable(dst *image.RGBA, renderedHeaders []busStopHeader, table busRenderTable, x, y, width, colW, headerH, rowH, scale int, headerFace, headerEmphasisFace, bodyFace, monoFace font.Face, headerBg, rowBg, highlightBg, line, ink, muted, departed, accent color.RGBA) {
	height := busTableHeight(table, headerH, rowH)
	cols := max(1, len(table.Header))

	drawRect(dst, image.Rect(x, y, x+width, y+height), rowBg)
	drawRect(dst, image.Rect(x, y, x+width, y+headerH), headerBg)
	for ri, row := range table.Rows {
		rowY := y + headerH + ri*rowH
		if row.Highlight {
			drawRect(dst, image.Rect(x, rowY, x+width, rowY+rowH), highlightBg)
		} else if ri%2 == 1 {
			drawRect(dst, image.Rect(x, rowY, x+width, rowY+rowH), color.RGBA{248, 250, 252, 255})
		}
	}

	for ci := 0; ci <= cols; ci++ {
		lineX := x + ci*colW
		if ci == cols {
			lineX = x + width - scale
		}
		drawRect(dst, image.Rect(lineX, y, lineX+scale, y+height), line)
	}
	for ri := 0; ri <= len(table.Rows)+1; ri++ {
		lineY := y
		switch {
		case ri == 0:
			lineY = y
		case ri == 1:
			lineY = y + headerH
		case ri == len(table.Rows)+1:
			lineY = y + height - scale
		default:
			lineY = y + headerH + (ri-1)*rowH
		}
		drawRect(dst, image.Rect(x, lineY, x+width, lineY+scale), line)
	}

	for i, header := range renderedHeaders {
		cellX := x + i*colW
		face := headerFace
		textColor := muted
		if header.Emphasize {
			face = headerEmphasisFace
			textColor = ink
		}
		drawCenteredText(dst, face, cellX, cellX+colW, y+24*scale, header.Text, textColor)
	}
	for ri, row := range table.Rows {
		rowY := y + headerH + ri*rowH
		textColor := ink
		if row.Highlight {
			textColor = accent
		} else if row.Departed {
			textColor = departed
		}
		for ci := 0; ci < cols; ci++ {
			cell := ""
			if ci < len(row.Cells) {
				cell = row.Cells[ci]
			}
			cellX := x + ci*colW
			cellFace := bodyFace
			if busCellIsNumeric(cell) {
				cellFace = monoFace
			}
			drawCenteredText(dst, cellFace, cellX, cellX+colW, rowY+29*scale, cell, textColor)
		}
	}
}

func drawBusLogoWatermark(dst *image.RGBA, bounds image.Rectangle, size int, opacity float64) {
	logo := loadBusLogo()
	if logo == nil {
		return
	}
	step := size * 3 / 2
	// build a larger tile layer to allow rotation without clipping
	diag := int(math.Ceil(math.Sqrt(float64(bounds.Dx()*bounds.Dx() + bounds.Dy()*bounds.Dy()))))
	layer := image.NewRGBA(image.Rect(0, 0, diag, diag))
	draw.Draw(layer, layer.Bounds(), &image.Uniform{C: color.RGBA{0, 0, 0, 0}}, image.Point{}, draw.Src)
	center := bounds.Min.Add(bounds.Max).Div(2)
	startX := center.X - diag/2
	startY := center.Y - diag/2
	for x := startX; x < startX+diag+size; x += step {
		col := (x - startX) / step
		yOffset := 0
		if col%2 == 1 {
			yOffset = step / 2
		}
		for y := startY - yOffset; y < startY+diag+size; y += step {
			drawLogoTile(layer, logo, x+size/2, y+size/2, size, opacity)
		}
	}
	rotated := rotateLayer(layer, -math.Pi/6)
	draw.Draw(dst, bounds, rotated, image.Point{X: (rotated.Bounds().Dx() - bounds.Dx()) / 2, Y: (rotated.Bounds().Dy() - bounds.Dy()) / 2}, draw.Over)
}

func rotateLayer(src *image.RGBA, angle float64) *image.RGBA {
	bounds := src.Bounds()
	centerX := float64(bounds.Dx()) / 2
	centerY := float64(bounds.Dy()) / 2
	cosA := math.Cos(angle)
	sinA := math.Sin(angle)
	dst := image.NewRGBA(bounds)
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			dx := float64(x) - centerX
			dy := float64(y) - centerY
			// inverse rotation: sample from source at rotated coordinates
			u := dx*cosA + dy*sinA + centerX
			v := -dx*sinA + dy*cosA + centerY
			ui, vi := int(u), int(v)
			if ui < bounds.Min.X || ui >= bounds.Max.X || vi < bounds.Min.Y || vi >= bounds.Max.Y {
				continue
			}
			dst.SetRGBA(x, y, src.RGBAAt(ui, vi))
		}
	}
	return dst
}

func drawLogoTile(dst *image.RGBA, src image.Image, centerX, centerY, size int, opacity float64) {
	bounds := src.Bounds()
	if bounds.Empty() || size <= 0 || opacity <= 0 {
		return
	}
	radius := size / 2
	half := float64(size) / 2
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			dx := float64(x - centerX)
			dy := float64(y - centerY)
			u := dx + half
			v := dy + half
			if u < 0 || v < 0 || u >= float64(size) || v >= float64(size) {
				continue
			}
			sx := bounds.Min.X + int(u*float64(bounds.Dx())/float64(size))
			sy := bounds.Min.Y + int(v*float64(bounds.Dy())/float64(size))
			blendPixel(dst, x, y, src.At(sx, sy), opacity)
		}
	}
}

func (r Renderer) fontFace(size float64) (font.Face, error) {
	return r.loadFont(defaultFontPaths, size)
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
	face, err := r.loadFont(busMonoFontPaths, size)
	if err == nil {
		return face, nil
	}
	return r.sansFontFace(size)
}

func (r Renderer) loadFont(candidates []string, size float64) (font.Face, error) {
	path := strings.TrimSpace(r.FontPath)
	if path == "" {
		for _, candidate := range candidates {
			if fileExists(candidate) {
				path = candidate
				break
			}
		}
	}
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

func busStopHeaders(headers []string, title string) []busStopHeader {
	if len(headers) == 0 {
		return nil
	}
	from, to := parseBusEndpoints(title)
	out := make([]busStopHeader, len(headers))
	for i, h := range headers {
		h = strings.TrimSpace(h)
		out[i].Text = h
		if h == from && h != "" {
			out[i].Emphasize = true
		}
		if h == to && h != "" && h != from {
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
	const (
		canvasWidth      = 920
		leftMargin       = 52
		rightMargin      = 52
		gap              = 28
		tableTop         = 80
		headerH          = 34
		rowH             = 42
		marginBelowTable = 24
		footerLineGap    = 18
		marginBottom     = 10
	)
	columnWidth := 112
	tablePositions := make([]image.Point, 0, len(tables))
	tableBottom := tableTop
	x := leftMargin
	y := tableTop
	rowBottom := tableTop
	for _, table := range tables {
		tableW := len(table.Header) * columnWidth
		tableH := headerH + len(table.Rows)*rowH
		if x > leftMargin && x+tableW > canvasWidth-rightMargin {
			x = leftMargin
			y = rowBottom + gap
		}
		tablePositions = append(tablePositions, image.Point{X: x, Y: y})
		bottom := y + tableH
		if bottom > rowBottom {
			rowBottom = bottom
		}
		if bottom > tableBottom {
			tableBottom = bottom
		}
		x += tableW + gap
	}
	footerY := tableBottom + marginBelowTable
	height := footerY + footerLineGap + marginBottom
	nextTime, nextWait := busNextWait(tables, "校车 · "+busRenderTitle(img), now)
	layout := busRenderLayout{
		ColumnWidth: columnWidth,
		TableTop:    tableTop,
		HeaderLines: []string{
			"Life @ USTC",
			"校车 · " + busRenderTitle(img),
		},
		FooterLines: []string{
			now.Format("2006-01-02 15:04") + "（" + busDayType(now) + "）",
			"2026 春季学期时刻表 / 蜗壳小道消息",
		},
		NextTime:       nextTime,
		NextWait:       nextWait,
		TableBottom:    tableBottom,
		FooterY:        footerY,
		Height:         height,
		LogoOpacity:    0.15,
		LogoCenterX:    0,
		LogoCenterY:    0,
		LogoSize:       210,
		UsesSerifFont:  true,
		TableWidths:    []int{},
		TablePositions: tablePositions,
		TableHeaders:   [][]busStopHeader{},
	}
	for _, table := range tables {
		layout.TableWidths = append(layout.TableWidths, len(table.Header)*layout.ColumnWidth)
		layout.TableHeaders = append(layout.TableHeaders, busStopHeaders(table.Header, layout.HeaderLines[1]))
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
	case "deadlines":
		theme.Label = "截止"
		theme.Background = color.RGBA{255, 247, 237, 255}
		theme.Header = color.RGBA{255, 237, 213, 255}
		theme.Accent = color.RGBA{234, 88, 12, 255}
	case "schedule":
		theme.Label = "课表"
	}
	return theme
}

func drawRect(dst *image.RGBA, rect image.Rectangle, c color.Color) {
	draw.Draw(dst, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func drawText(dst *image.RGBA, face font.Face, x, y int, text string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(drawableText(face, text))
}

func drawCenteredText(dst *image.RGBA, face font.Face, left, right, y int, text string, c color.Color) {
	text = drawableText(face, text)
	x := left + (right-left-textWidth(face, text))/2
	if x < left+4 {
		x = left + 4
	}
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func drawRightText(dst *image.RGBA, face font.Face, right, y int, text string, c color.Color) {
	text = drawableText(face, text)
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(right-textWidth(face, text), y),
	}
	d.DrawString(text)
}

func textWidth(face font.Face, text string) int {
	return font.MeasureString(face, text).Ceil()
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

func drawableText(face font.Face, text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\t':
			out.WriteString("  ")
		case '\n':
			out.WriteRune(r)
		case '\r':
			continue
		default:
			if r < ' ' {
				out.WriteRune(' ')
				continue
			}
			if _, ok := face.GlyphAdvance(r); !ok {
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func wrappedLines(lines []string, maxRunes int) []string {
	out := []string{}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		runes := []rune(line)
		for len(runes) > maxRunes {
			out = append(out, string(runes[:maxRunes]))
			runes = runes[maxRunes:]
		}
		out = append(out, string(runes))
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
