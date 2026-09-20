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

// remoteBusPayload is the semantic payload for kind "bus". The sidecar owns
// the fixed phone canvas and lays every route table out in normal flow.
type remoteBusPayload struct {
	Title    string           `json:"title"`
	NextTime string           `json:"next_time,omitempty"`
	NextWait string           `json:"next_wait,omitempty"`
	Footer   []string         `json:"footer,omitempty"`
	Tables   []remoteBusTable `json:"tables"`
}

type remoteBusTable struct {
	Label          string         `json:"label,omitempty"`
	Header         []string       `json:"header,omitempty"`
	HeaderEmphasis []bool         `json:"header_emphasis,omitempty"`
	Rows           []remoteBusRow `json:"rows"`
}

type remoteBusRow struct {
	Cells     []string `json:"cells"`
	Highlight bool     `json:"highlight,omitempty"`
	Departed  bool     `json:"departed,omitempty"`
}

// remoteRichPayload is the semantic payload for kind "rich". Text remains
// whole so Typst can wrap it at the content width.
type remoteRichPayload struct {
	Title  string            `json:"title"`
	Footer []string          `json:"footer,omitempty"`
	Blocks []remoteRichBlock `json:"blocks"`
}

// remoteRichBlock is one semantic document block: either text lines or a table.
type remoteRichBlock struct {
	Heading string           `json:"heading,omitempty"`
	Lines   []string         `json:"lines,omitempty"`
	Table   *remoteRichTable `json:"table,omitempty"`
}

type remoteRichTable struct {
	Header         []string        `json:"header,omitempty"`
	HeaderEmphasis []bool          `json:"header_emphasis,omitempty"`
	Rows           []remoteRichRow `json:"rows"`
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
	defer func() { _ = resp.Body.Close() }()

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

// buildBusRequest parses the rich bus document, preserves its table content,
// and applies the same departure/highlight semantics as the local renderer.
// Geometry stays in the Typst template so long stop names and times can wrap
// at the content width.
func (r RemoteRenderer) buildBusRequest(img *Image) remoteBusPayload {
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	doc := parseRichText(img.RichText)
	tables := make([]busRenderTable, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		if block.Table != nil {
			tables = append(tables, *block.Table)
		}
	}
	markBusRowsByTime(tables, doc.Title, now)
	nextTime, nextWait := busNextWait(tables, doc.Title, now)
	req := remoteBusPayload{
		// Keep the sidecar title byte-for-byte identical to the parsed rich
		// document, including all-routes and route-specific forms.
		Title:    doc.Title,
		NextTime: nextTime,
		NextWait: nextWait,
	}
	footer := richFooterLines(now, img.Ref)
	req.Footer = []string{footer[0], footer[1]}
	for _, block := range doc.Blocks {
		if block.Table == nil {
			continue
		}
		table := tables[len(req.Tables)]
		label := strings.TrimSpace(block.Heading)
		rows := make([]remoteBusRow, 0, len(table.Rows))
		for _, row := range table.Rows {
			rows = append(rows, remoteBusRow{
				Cells:     append([]string(nil), row.Cells...),
				Highlight: row.Highlight,
				Departed:  row.Departed,
			})
		}
		req.Tables = append(req.Tables, remoteBusTable{
			Label:          label,
			Header:         append([]string(nil), table.Header...),
			HeaderEmphasis: append([]bool(nil), table.HeaderEmphasis...),
			Rows:           rows,
		})
	}
	return req
}

// buildRichRequest preserves the parsed rich document and leaves wrapping and
// table sizing to Typst's normal-flow layout at the content width.
func (r RemoteRenderer) buildRichRequest(img *Image) remoteRichPayload {
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	doc := parseRichText(img.RichText)
	req := remoteRichPayload{
		Title: doc.Title,
	}
	footer := richFooterLines(now, img.Ref)
	req.Footer = []string{footer[0], footer[1]}
	for _, source := range doc.Blocks {
		block := remoteRichBlock{Heading: strings.TrimSpace(source.Heading)}
		if source.Table == nil {
			block.Lines = append([]string(nil), source.Lines...)
			req.Blocks = append(req.Blocks, block)
			continue
		}
		table := source.Table
		rows := make([]remoteRichRow, 0, len(table.Rows))
		for _, row := range table.Rows {
			rows = append(rows, remoteRichRow{
				Cells:     append([]string(nil), row.Cells...),
				Highlight: row.Highlight,
			})
		}
		block.Table = &remoteRichTable{
			Header:         append([]string(nil), table.Header...),
			HeaderEmphasis: append([]bool(nil), table.HeaderEmphasis...),
			Rows:           rows,
		}
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
