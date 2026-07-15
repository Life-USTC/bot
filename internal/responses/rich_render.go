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

const (
	richMetaFontSize = 9
	richNextFontSize = richMetaFontSize + 2
)

type richRenderMetrics struct {
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
	if isBus && richDocumentHasOnlyTables(doc) {
		return layoutRichBusTables(doc, m, nextTime, nextWait)
	}
	contentWidth := measureRichDocument(doc, m)
	if nextTime != "" {
		headerWidth := richTextWidth(doc.Title, 18) + richTextWidth("下一班 "+nextTime+" "+nextWait, richNextFontSize) + 40
		contentWidth = max(contentWidth, headerWidth)
	}
	canvasWidth := contentWidth + 2*m.MarginX
	y := m.ContentTop
	nodes := []richLayoutNode{}
	lastGap := 0
	for _, block := range doc.Blocks {
		if block.Table == nil {
			lines := make([]string, 0, len(block.Lines))
			for _, line := range block.Lines {
				if strings.TrimSpace(line) != "" {
					lines = append(lines, line)
				}
			}
			if block.Heading == "" && len(lines) == 0 {
				continue
			}
			height := len(lines) * m.TextRowHeight
			if block.Heading != "" {
				height += m.TableHeaderHeight
			}
			nodes = append(nodes, richLayoutNode{
				Bounds:  image.Rect(m.MarginX, y, m.MarginX+measureRichBlockWidth(block, m), y+height),
				Heading: block.Heading,
				Lines:   lines,
			})
			y += height + m.BlockGap
			lastGap = m.BlockGap
			continue
		}
		table := block.Table
		columnWidths := measureRichTableColumnWidths(*table, m)
		width := sumRichWidths(columnWidths)
		height := m.TableHeaderHeight + len(table.Rows)*m.TableRowHeight
		if block.Heading != "" {
			height += m.TableHeaderHeight
		}
		nodes = append(nodes, richLayoutNode{
			Bounds:       image.Rect(m.MarginX, y, m.MarginX+width, y+height),
			Heading:      block.Heading,
			Table:        table,
			Header:       richTableHeaders(*table),
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

func richDocumentHasOnlyTables(doc richDocument) bool {
	if len(doc.Blocks) == 0 {
		return false
	}
	for _, block := range doc.Blocks {
		if block.Table == nil {
			return false
		}
	}
	return true
}

func layoutRichBusTables(doc richDocument, m richRenderMetrics, nextTime, nextWait string) richLayout {
	measured := make([]richLayoutNode, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		table := block.Table
		columnWidths := measureRichTableColumnWidths(*table, m)
		width := sumRichWidths(columnWidths)
		height := m.TableHeaderHeight + len(table.Rows)*m.TableRowHeight
		if block.Heading != "" {
			height += m.TableHeaderHeight
		}
		measured = append(measured, richLayoutNode{
			Bounds:       image.Rect(0, 0, width, height),
			Heading:      block.Heading,
			Table:        table,
			Header:       richTableHeaders(*table),
			Label:        table.directionKey(),
			ColumnWidths: columnWidths,
		})
	}

	contentWidth := max(richTextWidth(doc.Title, 18), richTextWidth("15:04 · 工作日", richMetaFontSize))
	if nextTime != "" {
		headerWidth := richTextWidth(doc.Title, 18) + richTextWidth("下一班 "+nextTime+" "+nextWait, richNextFontSize) + 40
		contentWidth = max(contentWidth, headerWidth)
	}
	rows := groupRichBusTableNodes(measured)
	for _, row := range rows {
		contentWidth = max(contentWidth, richTableRowWidth(row, m.TableColumnGap))
	}
	canvasWidth := contentWidth + 2*m.MarginX

	y := m.ContentTop
	nodes := make([]richLayoutNode, 0, len(measured))
	for _, row := range rows {
		x := m.MarginX
		rowHeight := 0
		for _, node := range row {
			width, height := node.Bounds.Dx(), node.Bounds.Dy()
			node.Bounds = image.Rect(x, y, x+width, y+height)
			nodes = append(nodes, node)
			x += width + m.TableColumnGap
			rowHeight = max(rowHeight, height)
		}
		y += rowHeight + m.TableRowGap
	}
	if len(rows) > 0 {
		y -= m.TableRowGap
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

func groupRichBusTableNodes(nodes []richLayoutNode) [][]richLayoutNode {
	highTech := make([]richLayoutNode, 0, len(nodes))
	other := make([]richLayoutNode, 0, len(nodes))
	for _, node := range nodes {
		if richBusTableServesCampus(node.Table, "高新区") {
			highTech = append(highTech, node)
		} else {
			other = append(other, node)
		}
	}
	highTech = orderRichBusTablePairs(highTech)
	other = orderRichBusTablePairs(other)
	rows := make([][]richLayoutNode, 0, 2)
	if len(highTech) > 0 {
		rows = append(rows, highTech)
	}
	if len(other) > 0 {
		rows = append(rows, other)
	}
	return rows
}

func orderRichBusTablePairs(nodes []richLayoutNode) []richLayoutNode {
	ordered := make([]richLayoutNode, 0, len(nodes))
	used := make([]bool, len(nodes))
	for i, node := range nodes {
		if used[i] {
			continue
		}
		ordered = append(ordered, node)
		used[i] = true
		if node.Table == nil {
			continue
		}
		for j := i + 1; j < len(nodes); j++ {
			if used[j] || nodes[j].Table == nil {
				continue
			}
			if isReverseRoute(node.Table.Header, nodes[j].Table.Header) {
				ordered = append(ordered, nodes[j])
				used[j] = true
				break
			}
		}
	}
	return ordered
}

func richBusTableServesCampus(table *busRenderTable, campus string) bool {
	if table == nil {
		return false
	}
	for _, header := range table.Header {
		if strings.TrimSpace(header) == campus {
			return true
		}
	}
	return false
}

func richTableRowWidth(row []richLayoutNode, gap int) int {
	width := 0
	for i, node := range row {
		if i > 0 {
			width += gap
		}
		width += node.Bounds.Dx()
	}
	return width
}

func richDocumentIsBus(doc richDocument) bool {
	title := strings.TrimSpace(doc.Title)
	return title == "校车" || strings.HasPrefix(title, "校车 ")
}

func richTableHeaders(table busRenderTable) []busStopHeader {
	out := make([]busStopHeader, len(table.Header))
	for i, header := range table.Header {
		out[i] = busStopHeader{Text: strings.TrimSpace(header)}
		if i < len(table.HeaderEmphasis) {
			out[i].Emphasize = table.HeaderEmphasis[i]
		}
	}
	return out
}

func measureRichBlockWidth(block richBlock, metrics richRenderMetrics) int {
	width := 0
	if block.Heading != "" {
		width = richTextWidth(block.Heading, 13) + 2*metrics.TextPaddingX
	}
	if block.Table != nil {
		return max(width, sumRichWidths(measureRichTableColumnWidths(*block.Table, metrics)))
	}
	for _, line := range block.Lines {
		width = max(width, richTextWidth(line, 13)+2*metrics.TextPaddingX)
	}
	return width
}

func measureRichDocument(doc richDocument, metrics richRenderMetrics) int {
	width := richTextWidth(doc.Title, 18)
	for _, block := range doc.Blocks {
		width = max(width, measureRichBlockWidth(block, metrics))
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
		case r == '\u3000':
			width += float64(fontSize)
		default:
			width += float64(fontSize) * 0.62
		}
	}
	return int(width + 0.5)
}

type richFaces struct {
	Title     font.Face
	TitleMono font.Face
	Next      font.Face
	NextMono  font.Face
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
	next, err := load(r.sansFontFace, richNextFontSize)
	if err != nil {
		return richFaces{}, err
	}
	nextMono, err := load(r.monoFontFace, richNextFontSize)
	if err != nil {
		return richFaces{}, err
	}
	meta, err := load(r.sansFontFace, richMetaFontSize)
	if err != nil {
		return richFaces{}, err
	}
	metaMono, err := load(r.monoFontFace, richMetaFontSize)
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
		Next: next, NextMono: nextMono,
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
	departed := muted
	accent := color.RGBA{15, 118, 110, 255}
	drawRect(canvas, canvas.Bounds(), bg)
	drawBusLogoWatermark(canvas, canvas.Bounds(), s(120), 0.15)
	drawMixedText(canvas, faces.Title, faces.TitleMono, s(layout.Metrics.MarginX), s(layout.Metrics.TitleBaseline), layout.Title, ink)
	if layout.NextTime != "" {
		right := s(layout.Space.Width - layout.Metrics.MarginX)
		drawRightMixedText(canvas, faces.Next, faces.NextMono, right, s(layout.Metrics.TitleBaseline-18), "下一班 "+layout.NextTime, muted)
		drawRightMixedText(canvas, faces.Next, faces.NextMono, right, s(layout.Metrics.TitleBaseline), layout.NextWait, accent)
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
