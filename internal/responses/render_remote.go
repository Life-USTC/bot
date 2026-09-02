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

// RemoteRenderer is a proof-of-concept renderer that delegates PNG rendering
// to the typst-based sidecar service (renderd) over HTTP. It implements the
// same RenderPNG semantics as Renderer.
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
// pixel dimensions reported by the sidecar (falling back to decoding the PNG
// header if the headers are missing).
func (r RemoteRenderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	endpoint := strings.TrimSpace(r.Endpoint)
	if endpoint == "" {
		return nil, 0, 0, errors.New("remote renderer endpoint is empty")
	}
	if strings.TrimSpace(img.Kind) != "bus" {
		return nil, 0, 0, fmt.Errorf("remote renderer PoC only supports kind %q, got %q", "bus", img.Kind)
	}

	payload := r.buildBusRequest(img)
	if len(payload.Tables) == 0 {
		return nil, 0, 0, errors.New("response contains no bus tables")
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, 0, err
	}
	body, err := json.Marshal(remoteRenderRequest{Kind: img.Kind, Payload: payloadJSON})
	if err != nil {
		return nil, 0, 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "image/png")

	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("render sidecar unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, 0, fmt.Errorf("render sidecar returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}

	png, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, 0, 0, err
	}

	width := parseIntHeader(resp.Header, "X-Image-Width")
	height := parseIntHeader(resp.Header, "X-Image-Height")
	if width <= 0 || height <= 0 {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
		if err != nil {
			return nil, 0, 0, fmt.Errorf("decode rendered PNG header: %w", err)
		}
		width, height = cfg.Width, cfg.Height
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

	title := busRenderTitle(img)
	if title == "校车" {
		title = "全部路线"
	}

	req := remoteBusPayload{
		Title:        "校车 · " + title,
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

func parseIntHeader(h http.Header, name string) int {
	value := strings.TrimSpace(h.Get(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}
