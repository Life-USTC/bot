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
	TitleBaseline     int
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
		TitleBaseline:     50,
		ContentTop:        82,
		BlockGap:          30,
		TextRowHeight:     32,
		TableHeaderHeight: 28,
		TableRowHeight:    32,
		TextPaddingX:      8,
		TableCellPaddingX: 8,
		TableColumnGap:    20,
		TableRowGap:       30,
		FooterGap:         24,
		FooterLineGap:     14,
		BottomMargin:      32,
	}
}

type richLayoutNode struct {
	Bounds       image.Rectangle
	Heading      string
	Lines        []string
	Table        *busRenderTable
	Header       []busStopHeader
	Label        string
	ColumnWidths []int
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
	isBus := richDocumentIsBus(doc)
	tables := []busRenderTable{}
	if isBus {
		for _, block := range doc.Blocks {
			if block.Table != nil {
				tables = append(tables, *block.Table)
			}
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
			if block.Heading == "" && len(lines) == 0 {
				continue
			}
			height := len(lines) * m.TextRowHeight
			if block.Heading != "" {
				height += m.TableHeaderHeight
			}
			nodes = append(nodes, richLayoutNode{
				Bounds:  image.Rect(m.MarginX, y, m.MarginX+availableWidth, y+height),
				Heading: block.Heading,
				Lines:   lines,
			})
			y += height + m.BlockGap
			lastGap = m.BlockGap
			continue
		}
		table := block.Table
		columnWidths := fitRichTableColumnWidths(measureRichTableColumnWidths(*table, m), availableWidth)
		width := sumRichWidths(columnWidths)
		height := m.TableHeaderHeight + len(table.Rows)*m.TableRowHeight
		if block.Heading != "" {
			height += m.TableHeaderHeight
		}
		nodes = append(nodes, richLayoutNode{
			Bounds:       image.Rect(m.MarginX, y, m.MarginX+width, y+height),
			Heading:      block.Heading,
			Table:        table,
			Header:       richTableHeaders(table.Header, isBus),
			Label:        table.directionKey(),
			ColumnWidths: columnWidths,
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

func richDocumentIsBus(doc richDocument) bool {
	title := strings.TrimSpace(doc.Title)
	return title == "校车" || strings.HasPrefix(title, "校车 ")
}

func richTableHeaders(headers []string, emphasizeEndpoints bool) []busStopHeader {
	if emphasizeEndpoints {
		return busStopHeaders(headers, false)
	}
	out := make([]busStopHeader, len(headers))
	for i, header := range headers {
		out[i].Text = strings.TrimSpace(header)
	}
	return out
}

func measureRichDocument(doc richDocument, metrics richRenderMetrics) int {
	width := richTextWidth(doc.Title, 18)
	for _, block := range doc.Blocks {
		if block.Heading != "" {
			width = max(width, richTextWidth(block.Heading, 13)+2*metrics.TextPaddingX)
		}
		if block.Table != nil {
			width = max(width, sumRichWidths(measureRichTableColumnWidths(*block.Table, metrics)))
			continue
		}
		for _, line := range block.Lines {
			width = max(width, richTextWidth(line, 13)+2*metrics.TextPaddingX)
		}
	}
	// The timestamp and source footer are right-aligned on separate lines.
	return max(width, richTextWidth("15:04 · 工作日", 9))
}

func richFooterLines(now time.Time) [2]string {
	return [2]string{now.Format("15:04") + " · " + busDayType(now), "Life@USTC"}
}

func measureRichTableColumnWidths(table busRenderTable, metrics richRenderMetrics) []int {
	widths := make([]int, len(table.Header))
	for i, cell := range table.Header {
		widths[i] = richTextWidth(cell, 13)
	}
	for _, row := range table.Rows {
		for i, cell := range row.Cells {
			if i < len(widths) {
				widths[i] = max(widths[i], richTextWidth(cell, 14))
			}
		}
	}
	for i := range widths {
		widths[i] += 2 * metrics.TableCellPaddingX
	}
	return widths
}

func fitRichTableColumnWidths(widths []int, maxWidth int) []int {
	fitted := append([]int(nil), widths...)
	total := sumRichWidths(fitted)
	if len(fitted) == 0 || total <= maxWidth {
		return fitted
	}

	used := 0
	for i, width := range fitted {
		fitted[i] = width * maxWidth / total
		used += fitted[i]
	}
	for i := 0; used < maxWidth; i = (i + 1) % len(fitted) {
		fitted[i]++
		used++
	}
	return fitted
}

func sumRichWidths(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	return total
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
	Title     font.Face
	TitleMono font.Face
	Meta      font.Face
	MetaMono  font.Face
	Body      font.Face
	BodyMono  font.Face
	Head      font.Face
	HeadMono  font.Face
	Bold      font.Face
	BoldMono  font.Face
	Mono      font.Face
}

func (r Renderer) richFaces(scale int) (richFaces, error) {
	load := func(fn func(float64) (font.Face, error), size int) (font.Face, error) {
		return fn(float64(size * scale))
	}
	title, err := load(r.sansBoldFontFace, 18)
	if err != nil {
		return richFaces{}, err
	}
	titleMono, err := load(r.monoBoldFontFace, 18)
	if err != nil {
		return richFaces{}, err
	}
	meta, err := load(r.sansFontFace, 9)
	if err != nil {
		return richFaces{}, err
	}
	metaMono, err := load(r.monoFontFace, 9)
	if err != nil {
		return richFaces{}, err
	}
	body, err := load(r.sansFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	bodyMono, err := load(r.monoFontFace, 13)
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
	boldMono, err := load(r.monoBoldFontFace, 13)
	if err != nil {
		return richFaces{}, err
	}
	mono, err := load(r.monoFontFace, 14)
	if err != nil {
		return richFaces{}, err
	}
	return richFaces{
		Title: title, TitleMono: titleMono,
		Meta: meta, MetaMono: metaMono,
		Body: body, BodyMono: bodyMono,
		Head: head, HeadMono: bodyMono,
		Bold: bold, BoldMono: boldMono,
		Mono: mono,
	}, nil
}

func (r Renderer) renderRichPNG(text string) ([]byte, int, int, error) {
	doc := parseRichText(text)
	now := time.Now().In(time.FixedZone("CST", 8*60*60))
	tables := []busRenderTable{}
	if richDocumentIsBus(doc) {
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
	}
	layout := layoutRichText(doc, now)
	s := layout.Space.px
	faces, err := r.richFaces(layout.Space.Scale)
	if err != nil {
		return nil, 0, 0, err
	}
	canvas := image.NewRGBA(layout.Space.bounds())
	bg := color.RGBA{250, 250, 250, 255}
	ink := color.RGBA{39, 39, 42, 255}
	muted := color.RGBA{113, 113, 122, 255}
	line := color.RGBA{212, 212, 216, 255}
	rowBg := bg
	headBg := bg
	highlightBg := color.RGBA{244, 244, 245, 255}
	departed := color.RGBA{132, 132, 132, 255}
	accent := color.RGBA{15, 118, 110, 255}
	drawRect(canvas, canvas.Bounds(), bg)
	drawBusLogoWatermark(canvas, canvas.Bounds(), s(120), 0.15)
	drawMixedText(canvas, faces.Title, faces.TitleMono, s(layout.Metrics.MarginX), s(layout.Metrics.TitleBaseline), layout.Title, ink)
	if layout.NextTime != "" {
		right := s(layout.Space.Width - layout.Metrics.MarginX)
		drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, s(layout.Metrics.TitleBaseline-18), "下一班 "+layout.NextTime, muted)
		drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, s(layout.Metrics.TitleBaseline), layout.NextWait, accent)
	}
	for _, node := range layout.Nodes {
		x, y := s(node.Bounds.Min.X), s(node.Bounds.Min.Y)
		if node.Table != nil {
			tableY := y
			if node.Heading != "" {
				drawMixedText(canvas, faces.Bold, faces.BoldMono, x+s(layout.Metrics.TextPaddingX), y+s(layout.Metrics.TableHeaderHeight/2+5), node.Heading, ink)
				tableY += s(layout.Metrics.TableHeaderHeight)
				drawRect(canvas, image.Rect(x, tableY, s(node.Bounds.Max.X), tableY+layout.Space.Scale), line)
			}
			columnWidths := make([]int, len(node.ColumnWidths))
			for i, width := range node.ColumnWidths {
				columnWidths[i] = s(width)
			}
			drawBusTable(canvas, node.Header, *node.Table, x, tableY, s(node.Bounds.Dx()), columnWidths, s(layout.Metrics.TableHeaderHeight), s(layout.Metrics.TableRowHeight), s(layout.Metrics.TableCellPaddingX), layout.Space.Scale, faces.Head, faces.HeadMono, faces.Bold, faces.BoldMono, faces.Body, faces.Mono, headBg, rowBg, highlightBg, line, ink, departed, ink)
			continue
		}
		drawRect(canvas, image.Rect(x, y, s(node.Bounds.Max.X), s(node.Bounds.Max.Y)), rowBg)
		rowY := y
		if node.Heading != "" {
			drawMixedText(canvas, faces.Bold, faces.BoldMono, x+s(layout.Metrics.TextPaddingX), y+s(layout.Metrics.TableHeaderHeight/2+5), node.Heading, ink)
			rowY += s(layout.Metrics.TableHeaderHeight)
			if len(node.Lines) > 0 {
				drawRect(canvas, image.Rect(x, rowY, s(node.Bounds.Max.X), rowY+layout.Space.Scale), line)
			}
		}
		for i, text := range node.Lines {
			lineY := rowY + i*s(layout.Metrics.TextRowHeight)
			drawMixedText(canvas, faces.Body, faces.BodyMono, x+s(layout.Metrics.TextPaddingX), lineY+s(layout.Metrics.TextRowHeight/2+5), text, ink)
			if i < len(node.Lines)-1 {
				separatorY := lineY + s(layout.Metrics.TextRowHeight)
				drawRect(canvas, image.Rect(x, separatorY, s(node.Bounds.Max.X), separatorY+layout.Space.Scale), line)
			}
		}
	}
	footerY := s(layout.FooterY)
	right := s(layout.Space.Width - layout.Metrics.MarginX)
	footerLines := richFooterLines(now)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, footerY, footerLines[0], muted)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, right, footerY+s(layout.Metrics.FooterLineGap), footerLines[1], muted)
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), canvas.Bounds().Dx(), canvas.Bounds().Dy(), nil
}
