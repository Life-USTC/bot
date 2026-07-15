package responses

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"time"

	"golang.org/x/image/font"
)

type richRenderMetrics struct {
	Width             int
	Scale             int
	MarginX           int
	HeaderTop         int
	ContentTop        int
	BlockGap          int
	TextRowHeight     int
	TableHeaderHeight int
	TableRowHeight    int
	TableColumnWidth  int
	TableColumnGap    int
	TableRowGap       int
	FooterGap         int
	FooterLineGap     int
	BottomMargin      int
}

func defaultRichRenderMetrics() richRenderMetrics {
	return richRenderMetrics{
		Width:             920,
		Scale:             2,
		MarginX:           52,
		HeaderTop:         28,
		ContentTop:        84,
		BlockGap:          20,
		TextRowHeight:     42,
		TableHeaderHeight: 28,
		TableRowHeight:    32,
		TableColumnWidth:  120,
		TableColumnGap:    20,
		TableRowGap:       30,
		FooterGap:         24,
		FooterLineGap:     14,
		BottomMargin:      12,
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
	availableWidth := m.Width - 2*m.MarginX
	y := m.ContentTop
	nodes := []richLayoutNode{}
	for _, block := range doc.Blocks {
		if block.Table == nil {
			lines := []string{}
			for _, line := range block.Lines {
				lines = append(lines, wrapRichLine(line, 42)...)
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
			continue
		}
		table := block.Table
		width := len(table.Header) * m.TableColumnWidth
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
	}
	if len(nodes) > 0 {
		y -= m.BlockGap
	}
	footerY := y + m.FooterGap
	height := footerY + m.FooterLineGap + m.BottomMargin
	tables := []busRenderTable{}
	for _, block := range doc.Blocks {
		if block.Table != nil {
			tables = append(tables, *block.Table)
		}
	}
	nextTime, nextWait := busNextWait(tables, doc.Title, now)
	return richLayout{
		Space:    imageRenderSpace{Width: m.Width, Height: height, Scale: m.Scale},
		Metrics:  m,
		Title:    doc.Title,
		Nodes:    nodes,
		NextTime: nextTime,
		NextWait: nextWait,
		FooterY:  footerY,
	}
}

func wrapRichLine(line string, limit int) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	runes := []rune(line)
	if len(runes) <= limit {
		return []string{line}
	}
	out := []string{}
	for len(runes) > limit {
		out = append(out, string(runes[:limit]))
		runes = runes[limit:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
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
	brand, err := load(r.serifFontFace, 11)
	if err != nil {
		return richFaces{}, err
	}
	title, err := load(r.serifBoldFontFace, 18)
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
	head, err := load(r.serifFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	bold, err := load(r.serifBoldFontFace, 13)
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
	drawText(canvas, faces.Brand, s(layout.Metrics.MarginX), s(layout.Metrics.HeaderTop), "Life @ USTC", muted)
	drawText(canvas, faces.Title, s(layout.Metrics.MarginX), s(layout.Metrics.HeaderTop+26), layout.Title, ink)
	if layout.NextTime != "" {
		right := s(layout.Space.Width - layout.Metrics.MarginX)
		drawRightText(canvas, faces.Meta, right, s(layout.Metrics.HeaderTop), "下一班 "+layout.NextTime, muted)
		drawRightText(canvas, faces.Meta, right, s(layout.Metrics.HeaderTop+26), layout.NextWait, accent)
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
			drawText(canvas, faces.Body, x+s(20), rowY+s(28), text, ink)
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
