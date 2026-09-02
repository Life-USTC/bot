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

// remoteBusPayload is the payload for kind "bus".
type remoteBusPayload struct {
	Kind         string           `json:"kind"`
	Title        string           `json:"title"`
	DateLine     string           `json:"date_line"`
	SemesterLine string           `json:"semester_line"`
	Next         *remoteNextBus   `json:"next,omitempty"`
	Tables       []remoteBusTable `json:"tables"`
}

type remoteNextBus struct {
	Time string `json:"time"`
	Wait string `json:"wait"`
}

type remoteBusTable struct {
	Label          string         `json:"label,omitempty"`
	Header         []string       `json:"header"`
	HeaderEmphasis []bool         `json:"header_emphasis,omitempty"`
	Rows           []remoteBusRow `json:"rows"`
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

func (r RemoteRenderer) buildBusRequest(img *Image) remoteBusPayload {
	now := r.now().In(time.FixedZone("CST", 8*60*60))
	tables := busRenderTables(img)
	markBusRowsByTime(tables, img.Title, now)
	nextTime, nextWait := busNextWait(tables, img.Title, now)

	title := busRenderTitle(img)
	showDirectionLabels := title == "校车"
	if showDirectionLabels {
		title = "全部路线"
	}

	req := remoteBusPayload{
		Kind:         img.Kind,
		Title:        "校车 · " + title,
		DateLine:     now.Format("2006-01-02 15:04") + "（" + busDayType(now) + "）",
		SemesterLine: "2026 春季学期时刻表 / 蜗壳小道消息",
	}
	if nextTime != "" {
		req.Next = &remoteNextBus{Time: nextTime, Wait: nextWait}
	}
	for _, table := range tables {
		headers := busStopHeaders(table.Header, true)
		rt := remoteBusTable{Rows: make([]remoteBusRow, 0, len(table.Rows))}
		if showDirectionLabels {
			rt.Label = table.directionKey()
		}
		for _, h := range headers {
			rt.Header = append(rt.Header, h.Text)
			rt.HeaderEmphasis = append(rt.HeaderEmphasis, h.Emphasize)
		}
		for _, row := range table.Rows {
			rt.Rows = append(rt.Rows, remoteBusRow{
				Cells:     row.Cells,
				Highlight: row.Highlight,
				Departed:  row.Departed,
			})
		}
		req.Tables = append(req.Tables, rt)
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
