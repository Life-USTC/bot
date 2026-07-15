package responses

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/font"
)

type richRenderMetrics struct {
	MinWidth          int
	MaxWidth          int
	Scale             int
	MarginX           int
	BrandBaseline     int
	ContentTop        int
	BlockGap          int
	TextRowHeight     int
	TableHeaderHeight int
	TableRowHeight    int
	TextPaddingX      int
	TableCellPaddingX int
	TableColumnGap    int
	TableRowGap       int
	FooterGap         int
	FooterLineGap     int
	BottomMargin      int
}

func defaultRichRenderMetrics() richRenderMetrics {
	return richRenderMetrics{
		MinWidth:          320,
		MaxWidth:          920,
		Scale:             2,
		MarginX:           32,
		BrandBaseline:     43,
		ContentTop:        97,
		BlockGap:          20,
		TextRowHeight:     42,
		TableHeaderHeight: 28,
		TableRowHeight:    32,
		TextPaddingX:      20,
		TableCellPaddingX: 14,
		TableColumnGap:    20,
		TableRowGap:       30,
		FooterGap:         24,
		FooterLineGap:     14,
		BottomMargin:      32,
	}
}

type richLayoutNode struct {
	Bounds image.Rectangle
	Lines  []string
	Table  *busRenderTable
	Header []busStopHeader
	Label  string
	ColW   int
}

type richLayout struct {
	Space    imageRenderSpace
	Metrics  richRenderMetrics
	Title    string
	Nodes    []richLayoutNode
	NextTime string
	NextWait string
	FooterY  int
}

func layoutRichText(doc richDocument, now time.Time) richLayout {
	m := defaultRichRenderMetrics()
	tables := []busRenderTable{}
	for _, block := range doc.Blocks {
		if block.Table != nil {
			tables = append(tables, *block.Table)
		}
	}
	nextTime, nextWait := busNextWait(tables, doc.Title, now)
	contentWidth := measureRichDocument(doc, m)
	if nextTime != "" {
		headerWidth := richTextWidth(doc.Title, 18) + richTextWidth("下一班 "+nextTime+" "+nextWait, 9) + 40
		contentWidth = max(contentWidth, headerWidth)
	}
	canvasWidth := min(m.MaxWidth, max(m.MinWidth, contentWidth+2*m.MarginX))
	availableWidth := canvasWidth - 2*m.MarginX
	y := m.ContentTop
	nodes := []richLayoutNode{}
	lastGap := 0
	for _, block := range doc.Blocks {
		if block.Table == nil {
			lines := []string{}
			for _, line := range block.Lines {
				lines = append(lines, wrapRichLineToWidth(line, availableWidth-2*m.TextPaddingX, 13)...)
			}
			if len(lines) == 0 {
				continue
			}
			height := len(lines) * m.TextRowHeight
			nodes = append(nodes, richLayoutNode{
				Bounds: image.Rect(m.MarginX, y, m.MarginX+availableWidth, y+height),
				Lines:  lines,
			})
			y += height + m.BlockGap
			lastGap = m.BlockGap
			continue
		}
		table := block.Table
		columnWidth := measureRichTableColumnWidth(*table, m)
		width := len(table.Header) * columnWidth
		if width > availableWidth {
			width = availableWidth
		}
		colW := width / max(1, len(table.Header))
		height := m.TableHeaderHeight + len(table.Rows)*m.TableRowHeight
		nodes = append(nodes, richLayoutNode{
			Bounds: image.Rect(m.MarginX, y, m.MarginX+width, y+height),
			Table:  table,
			Header: busStopHeaders(table.Header, true),
			Label:  table.directionKey(),
			ColW:   colW,
		})
		y += height + m.TableRowGap
		lastGap = m.TableRowGap
	}
	if len(nodes) > 0 {
		y -= lastGap
	}
	footerY := y + m.FooterGap
	height := footerY + m.FooterLineGap + m.BottomMargin
	return richLayout{
		Space:    imageRenderSpace{Width: canvasWidth, Height: height, Scale: m.Scale},
		Metrics:  m,
		Title:    doc.Title,
		Nodes:    nodes,
		NextTime: nextTime,
		NextWait: nextWait,
		FooterY:  footerY,
	}
}

func measureRichDocument(doc richDocument, metrics richRenderMetrics) int {
	width := richTextWidth(doc.Title, 18)
	for _, block := range doc.Blocks {
		if block.Table != nil {
			width = max(width, len(block.Table.Header)*measureRichTableColumnWidth(*block.Table, metrics))
			continue
		}
		for _, line := range block.Lines {
			width = max(width, richTextWidth(line, 13)+2*metrics.TextPaddingX)
		}
	}
	// The timestamp and source footer are right-aligned on separate lines.
	return max(width, richTextWidth("2006-01-02 15:04（工作日）", 9))
}

func measureRichTableColumnWidth(table busRenderTable, metrics richRenderMetrics) int {
	width := 0
	for _, cell := range table.Header {
		width = max(width, richTextWidth(cell, 13))
	}
	for _, row := range table.Rows {
		for _, cell := range row.Cells {
			width = max(width, richTextWidth(cell, 14))
		}
	}
	return width + 2*metrics.TableCellPaddingX
}

func richTextWidth(text string, fontSize int) int {
	width := 0.0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			width += float64(fontSize)
		case unicode.IsSpace(r):
			width += float64(fontSize) * 0.35
		default:
			width += float64(fontSize) * 0.62
		}
	}
	return int(width + 0.5)
}

func wrapRichLineToWidth(line string, maxWidth, fontSize int) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if richTextWidth(line, fontSize) <= maxWidth {
		return []string{line}
	}
	out := []string{}
	current := []rune{}
	for _, r := range []rune(line) {
		candidate := append(current, r)
		if len(current) > 0 && richTextWidth(string(candidate), fontSize) > maxWidth {
			out = append(out, string(current))
			current = []rune{r}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		out = append(out, string(current))
	}
	return out
}

type richFaces struct {
	Brand font.Face
	Title font.Face
	Meta  font.Face
	Body  font.Face
	Head  font.Face
	Bold  font.Face
	Mono  font.Face
}

func (r Renderer) richFaces(scale int) (richFaces, error) {
	load := func(fn func(float64) (font.Face, error), size int) (font.Face, error) {
		return fn(float64(size * scale))
	}
	brand, err := load(r.sansFontFace, 11)
	if err != nil {
		return richFaces{}, err
	}
	title, err := load(r.sansBoldFontFace, 18)
	if err != nil {
		return richFaces{}, err
	}
	meta, err := load(r.sansFontFace, 9)
	if err != nil {
		return richFaces{}, err
	}
	body, err := load(r.sansFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	head, err := load(r.sansFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	bold, err := load(r.sansBoldFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	mono, err := load(r.monoFontFace, 14)
	if err != nil {
		return richFaces{}, err
	}
	return richFaces{Brand: brand, Title: title, Meta: meta, Body: body, Head: head, Bold: bold, Mono: mono}, nil
}

func (r Renderer) renderRichPNG(text string) ([]byte, int, int, error) {
	doc := parseRichText(text)
	now := time.Now().In(time.FixedZone("CST", 8*60*60))
	tables := []busRenderTable{}
	for i := range doc.Blocks {
		if doc.Blocks[i].Table != nil {
			tables = append(tables, *doc.Blocks[i].Table)
		}
	}
	markBusRowsByTime(tables, doc.Title, now)
	tableIndex := 0
	for i := range doc.Blocks {
		if doc.Blocks[i].Table != nil {
			*doc.Blocks[i].Table = tables[tableIndex]
			tableIndex++
		}
	}
	layout := layoutRichText(doc, now)
	s := layout.Space.px
	faces, err := r.richFaces(layout.Space.Scale)
	if err != nil {
		return nil, 0, 0, err
	}
	canvas := image.NewRGBA(layout.Space.bounds())
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
	drawBusLogoWatermark(canvas, canvas.Bounds(), s(120), 0.15)
	drawText(canvas, faces.Brand, s(layout.Metrics.MarginX), s(layout.Metrics.BrandBaseline), "Life @ USTC", muted)
	drawText(canvas, faces.Title, s(layout.Metrics.MarginX), s(layout.Metrics.BrandBaseline+26), layout.Title, ink)
	if layout.NextTime != "" {
		right := s(layout.Space.Width - layout.Metrics.MarginX)
		drawRightText(canvas, faces.Meta, right, s(layout.Metrics.BrandBaseline), "下一班 "+layout.NextTime, muted)
		drawRightText(canvas, faces.Meta, right, s(layout.Metrics.BrandBaseline+26), layout.NextWait, accent)
	}
	for _, node := range layout.Nodes {
		x, y := s(node.Bounds.Min.X), s(node.Bounds.Min.Y)
		if node.Table != nil {
			drawBusTable(canvas, node.Header, *node.Table, x, y, s(node.Bounds.Dx()), s(node.ColW), s(layout.Metrics.TableHeaderHeight), s(layout.Metrics.TableRowHeight), layout.Space.Scale, faces.Head, faces.Bold, faces.Body, faces.Mono, headBg, rowBg, highlightBg, line, ink, muted, departed, accent)
			continue
		}
		drawRect(canvas, image.Rect(x, y, s(node.Bounds.Max.X), s(node.Bounds.Max.Y)), rowBg)
		for i, text := range node.Lines {
			rowY := y + i*s(layout.Metrics.TextRowHeight)
			if i%2 == 1 {
				drawRect(canvas, image.Rect(x, rowY, s(node.Bounds.Max.X), rowY+s(layout.Metrics.TextRowHeight)), headBg)
			}
			drawText(canvas, faces.Body, x+s(layout.Metrics.TextPaddingX), rowY+s(28), text, ink)
			if i < len(node.Lines)-1 {
				drawRect(canvas, image.Rect(x, rowY+s(layout.Metrics.TextRowHeight)-layout.Space.Scale, s(node.Bounds.Max.X), rowY+s(layout.Metrics.TextRowHeight)), line)
			}
		}
	}
	footerY := s(layout.FooterY)
	right := s(layout.Space.Width - layout.Metrics.MarginX)
	drawRightText(canvas, faces.Meta, right, footerY, now.Format("2006-01-02 15:04")+"（"+busDayType(now)+"）", muted)
	drawRightText(canvas, faces.Meta, right, footerY+s(layout.Metrics.FooterLineGap), "Life @ USTC / 蜗壳小道消息", muted)
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), canvas.Bounds().Dx(), canvas.Bounds().Dy(), nil
}
