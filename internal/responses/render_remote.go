package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RemoteRenderer delegates PNG rendering to the typst-based renderd sidecar
// over HTTP. It implements the same RenderPNG semantics as Renderer.
type RemoteRenderer struct {
	// Endpoint is the full URL of the sidecar render endpoint, e.g.
	// "http://127.0.0.1:9123/render".
	Endpoint string
	// Client is the HTTP client used for requests. If nil, a default client
	// with a 10s timeout is used.
	Client *http.Client
	// Now, if non-nil, overrides the clock used for highlight/departed
	// computation (useful for deterministic output).
	Now func() time.Time
}

// remoteRenderRequest is the render envelope sent to the sidecar: a kind
// discriminator plus a kind-specific payload.
type remoteRenderRequest struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

const (
	maxRemotePNGBytes       = 32 << 20
	maxRemoteImageDimension = 16_384
	maxRemoteImagePixels    = 32 << 20
)

// remoteBusPayload is the payload for kind "bus". It mirrors the geometry of
// the legacy renderRichPNG bus branch: the sidecar only has to lay the tables
// out at the given widths, not re-measure anything.
type remoteBusPayload struct {
	Title        string           `json:"title"`
	ContentWidth int              `json:"content_width"`
	NextTime     string           `json:"next_time,omitempty"`
	NextWait     string           `json:"next_wait,omitempty"`
	Footer       []string         `json:"footer,omitempty"`
	RowsOfTables [][]int          `json:"rows_of_tables,omitempty"`
	Tables       []remoteBusTable `json:"tables,omitempty"`
}

type remoteBusTable struct {
	Label          string         `json:"label,omitempty"`
	Header         []string       `json:"header,omitempty"`
	HeaderEmphasis []bool         `json:"header_emphasis,omitempty"`
	ColumnWidths   []int          `json:"column_widths,omitempty"`
	Rows           []remoteBusRow `json:"rows,omitempty"`
}

type remoteBusRow struct {
	Cells     []string `json:"cells"`
	Highlight bool     `json:"highlight,omitempty"`
	Departed  bool     `json:"departed,omitempty"`
}

// remoteRichPayload is the payload for kind "rich" (every non-bus rich text
// card). It mirrors the legacy renderRichPNG general branch: the Go side runs
// layoutRichText and serializes each richLayoutNode into a block, so the
// sidecar draws final geometry without re-measuring.
type remoteRichPayload struct {
	Title             string            `json:"title"`
	ContentWidth      int               `json:"content_width"`
	CompactFirstBlock bool              `json:"compact_first_block"`
	Footer            []string          `json:"footer,omitempty"`
	Blocks            []remoteRichBlock `json:"blocks"`
}

// remoteRichBlock is one layout node: either a text block (Lines, already
// wrapped by wrapRichText — hence Wrapped) or a table block.
type remoteRichBlock struct {
	Heading string           `json:"heading,omitempty"`
	Lines   []string         `json:"lines,omitempty"`
	Wrapped bool             `json:"wrapped,omitempty"`
	Table   *remoteRichTable `json:"table,omitempty"`
}

type remoteRichTable struct {
	Header         []string        `json:"header,omitempty"`
	HeaderEmphasis []bool          `json:"header_emphasis,omitempty"`
	ColumnWidths   []int           `json:"column_widths,omitempty"`
	Rows           []remoteRichRow `json:"rows,omitempty"`
}

type remoteRichRow struct {
	Cells     []string `json:"cells"`
	Highlight bool     `json:"highlight,omitempty"`
}

func (r RemoteRenderer) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (r RemoteRenderer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// RenderPNG renders img via the sidecar. It returns the PNG bytes and the
// validated pixel dimensions reported by the sidecar (or the decoded image
// when the optional headers are missing).
func (r RemoteRenderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	return r.RenderPNGContext(context.Background(), img)
}

// RenderPNGContext is the cancellation-aware renderer entry point used by
// application workflows. The parent context can cancel an in-flight HTTP
// request when a job or notification times out.
func (r RemoteRenderer) RenderPNGContext(parent context.Context, img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	if parent != nil {
		select {
		case <-parent.Done():
			return nil, 0, 0, parent.Err()
		default:
		}
	}
	endpoint := strings.TrimSpace(r.Endpoint)
	if endpoint == "" {
		return nil, 0, 0, errors.New("remote renderer endpoint is empty")
	}
	var kind string
	var payload any
	switch {
	case img.Grid != nil:
		p := r.buildGridRequest(img)
		if len(p.Days) == 0 || len(p.Periods) == 0 {
			return nil, 0, 0, errors.New("response grid is empty")
		}
		kind = "grid"
		payload = p
	case img.Weather != nil:
		p, err := r.buildWeatherRequest(img)
		if err != nil {
			return nil, 0, 0, err
		}
		kind = "weather"
		payload = p
	case richDocumentIsBus(parseRichText(img.RichText)):
		p := r.buildBusRequest(img)
		if len(p.Tables) == 0 {
			return nil, 0, 0, errors.New("response contains no bus tables")
		}
		kind = "bus"
		payload = p
	default:
		if strings.TrimSpace(img.RichText) == "" {
			return nil, 0, 0, errors.New("response rich text is empty")
		}
		p := r.buildRichRequest(img)
		kind = "rich"
		payload = p
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, 0, err
	}
	body, err := json.Marshal(remoteRenderRequest{Kind: kind, Payload: payloadJSON})
	if err != nil {
		return nil, 0, 0, err
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "image/png")

	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, 0, ctxErr
		}
		return nil, 0, 0, fmt.Errorf("render sidecar unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, 0, fmt.Errorf("render sidecar returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}

	if resp.ContentLength > maxRemotePNGBytes {
		return nil, 0, 0, fmt.Errorf("render sidecar response is too large: %d bytes", resp.ContentLength)
	}
	png, err := io.ReadAll(io.LimitReader(resp.Body, maxRemotePNGBytes+1))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(png) == 0 {
		return nil, 0, 0, errors.New("render sidecar returned an empty PNG")
	}
	if len(png) > maxRemotePNGBytes {
		return nil, 0, 0, fmt.Errorf("render sidecar response is too large: more than %d bytes", maxRemotePNGBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode rendered PNG header: %w", err)
	}
	if format != "png" {
		return nil, 0, 0, fmt.Errorf("render sidecar returned %s instead of PNG", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, 0, 0, errors.New("render sidecar returned an image with invalid dimensions")
	}
	if cfg.Width > maxRemoteImageDimension || cfg.Height > maxRemoteImageDimension {
		return nil, 0, 0, fmt.Errorf("render sidecar image dimensions are too large: %dx%d", cfg.Width, cfg.Height)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxRemoteImagePixels {
		return nil, 0, 0, fmt.Errorf("render sidecar image has too many pixels: %dx%d", cfg.Width, cfg.Height)
	}
	// Decode the complete image after checking its advertised dimensions. DecodeConfig
	// only reads the header, so it would accept a truncated PNG with a valid IHDR.
	if _, format, err := image.Decode(bytes.NewReader(png)); err != nil {
		return nil, 0, 0, fmt.Errorf("decode rendered PNG: %w", err)
	} else if format != "png" {
		return nil, 0, 0, fmt.Errorf("render sidecar returned %s instead of PNG", format)
	}

	width, err := parseImageDimensionHeader(resp.Header, "X-Image-Width", cfg.Width)
	if err != nil {
		return nil, 0, 0, err
	}
	height, err := parseImageDimensionHeader(resp.Header, "X-Image-Height", cfg.Height)
	if err != nil {
		return nil, 0, 0, err
	}
	return png, width, height, nil
}

// buildBusRequest mirrors the legacy renderRichPNG bus branch: parse the rich
// text, mark departed/highlighted rows, and run the same layout pass so the
// sidecar receives final geometry (column widths, table pairing, content
// width) instead of re-deriving it.
func (r RemoteRenderer) buildBusRequest(img *Image) remoteBusPayload {
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	doc := parseRichText(img.RichText)
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

	req := remoteBusPayload{
		// renderRichPNG draws layout.Title directly. Keep the sidecar title
		// byte-for-byte identical to the parsed rich document, including the
		// all-routes and route-specific forms.
		Title:        layout.Title,
		ContentWidth: layout.Space.Width - 2*layout.Metrics.MarginX,
		NextTime:     layout.NextTime,
		NextWait:     layout.NextWait,
	}
	footer := richFooterLines(now)
	req.Footer = []string{footer[0], footer[1]}

	// Nodes are laid out row-major; group them back into visual rows and map
	// each table node to its index in doc order.
	indexOf := map[*busRenderTable]int{}
	for i := range doc.Blocks {
		if doc.Blocks[i].Table != nil {
			indexOf[doc.Blocks[i].Table] = len(req.Tables)
			req.Tables = append(req.Tables, remoteBusTable{})
		}
	}
	rowIndexByY := map[int]int{}
	for _, node := range layout.Nodes {
		if node.Table == nil {
			continue
		}
		idx, ok := indexOf[node.Table]
		if !ok {
			continue
		}
		rt := &req.Tables[idx]
		rt.Label = node.Heading
		rt.ColumnWidths = append([]int(nil), node.ColumnWidths...)
		for _, h := range node.Header {
			rt.Header = append(rt.Header, h.Text)
			rt.HeaderEmphasis = append(rt.HeaderEmphasis, h.Emphasize)
		}
		for _, row := range node.Table.Rows {
			rt.Rows = append(rt.Rows, remoteBusRow{
				Cells:     row.Cells,
				Highlight: row.Highlight,
				Departed:  row.Departed,
			})
		}
		y := node.Bounds.Min.Y
		rowIdx, seen := rowIndexByY[y]
		if !seen {
			rowIdx = len(req.RowsOfTables)
			rowIndexByY[y] = rowIdx
			req.RowsOfTables = append(req.RowsOfTables, nil)
		}
		req.RowsOfTables[rowIdx] = append(req.RowsOfTables[rowIdx], idx)
	}
	return req
}

// buildRichRequest mirrors the legacy renderRichPNG non-bus branch: parse the
// rich text, run the same layoutRichText pass, and serialize each layout node
// into a block carrying final geometry (pre-wrapped lines, fitted cells,
// column widths) so the sidecar only draws.
func (r RemoteRenderer) buildRichRequest(img *Image) remoteRichPayload {
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	doc := parseRichText(img.RichText)
	layout := layoutRichText(doc, now)
	m := layout.Metrics

	req := remoteRichPayload{
		Title:             layout.Title,
		ContentWidth:      layout.Space.Width - 2*m.MarginX,
		CompactFirstBlock: richDocumentHasCompactHelpIntro(doc),
	}
	footer := richFooterLines(now)
	req.Footer = []string{footer[0], footer[1]}

	for _, node := range layout.Nodes {
		block := remoteRichBlock{
			Heading: fitRichTextToWidth(node.Heading, node.Bounds.Dx()-2*m.TextPaddingX, 13),
		}
		if node.Table == nil {
			block.Lines = node.Lines
			block.Wrapped = true
			req.Blocks = append(req.Blocks, block)
			continue
		}
		table := &remoteRichTable{
			ColumnWidths: append([]int(nil), node.ColumnWidths...),
		}
		for i, header := range node.Header {
			width := 0
			if i < len(node.ColumnWidths) {
				width = node.ColumnWidths[i]
			}
			table.Header = append(table.Header, fitRichTextToWidth(header.Text, width-2*m.TableCellPaddingX, 13))
			table.HeaderEmphasis = append(table.HeaderEmphasis, header.Emphasize)
		}
		for _, row := range node.Table.Rows {
			cells := make([]string, len(table.Header))
			for i := range cells {
				cell := ""
				if i < len(row.Cells) {
					cell = row.Cells[i]
				}
				width := 0
				if i < len(node.ColumnWidths) {
					width = node.ColumnWidths[i]
				}
				cells[i] = fitRichTextToWidth(cell, width-2*m.TableCellPaddingX, 14)
			}
			table.Rows = append(table.Rows, remoteRichRow{Cells: cells, Highlight: row.Highlight})
		}
		block.Table = table
		req.Blocks = append(req.Blocks, block)
	}
	return req
}

func parseImageDimensionHeader(h http.Header, name string, actual int) (int, error) {
	value := strings.TrimSpace(h.Get(name))
	if value == "" {
		return actual, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("render sidecar returned invalid %s header", name)
	}
	if parsed != actual {
		return 0, fmt.Errorf("render sidecar %s header %d disagrees with PNG dimensions %d", name, parsed, actual)
	}
	return parsed, nil
}
