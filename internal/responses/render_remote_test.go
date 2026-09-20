package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testRemotePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{0, 128, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRemoteRendererRenderPNG(t *testing.T) {
	payload := testRemotePNG(t)
	var gotRequest remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return time.Date(2026, 9, 2, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}
	pngBytes, width, height, err := renderer.RenderPNG(testBusImage())
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	if !bytes.Equal(pngBytes, payload) {
		t.Fatalf("PNG bytes mismatch (got %d bytes, want %d)", len(pngBytes), len(payload))
	}
	if width != 8 || height != 6 {
		t.Fatalf("dimensions = %dx%d, want 8x6", width, height)
	}

	if gotRequest.Kind != "bus" {
		t.Fatalf("request kind = %q", gotRequest.Kind)
	}
	var gotPayload remoteBusPayload
	if err := json.Unmarshal(gotRequest.Payload, &gotPayload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if gotPayload.Title != "校车 东区 → 西区" {
		t.Fatalf("request title = %q", gotPayload.Title)
	}
	if len(gotPayload.Tables) != 2 {
		t.Fatalf("request tables = %d, want 2", len(gotPayload.Tables))
	}
	if got := gotPayload.Tables[0].Header; len(got) != 4 || got[0] != "东区" || got[3] != "高新区" {
		t.Fatalf("first table header = %#v", got)
	}
	if got := gotPayload.Tables[0].HeaderEmphasis; len(got) != 4 || got[0] || got[1] || got[2] || got[3] {
		t.Fatalf("first table header_emphasis = %#v, want [false false false false]", got)
	}
	if len(gotPayload.Tables[0].Rows) != 2 || !gotPayload.Tables[0].Rows[0].Highlight || gotPayload.Tables[0].Rows[0].Departed {
		t.Fatalf("first table semantic rows = %#v, want next row highlighted and not departed", gotPayload.Tables[0].Rows)
	}
	if got := gotPayload.Tables[1].Rows; len(got) != 2 || !got[0].Highlight || got[0].Departed {
		t.Fatalf("second table semantic rows = %#v, want explicit highlight preserved", got)
	}
	if len(gotPayload.Footer) != 2 || gotPayload.Footer[1] != "Life @ USTC" {
		t.Fatalf("footer = %#v", gotPayload.Footer)
	}
	if gotPayload.NextTime == "" {
		t.Fatalf("request next_time empty, want next bus hint")
	}
	if raw := string(gotRequest.Payload); strings.Contains(raw, "content_width") || strings.Contains(raw, "column_widths") || strings.Contains(raw, "rows_of_tables") {
		t.Fatalf("payload contains obsolete geometry: %s", raw)
	}
}

func TestRemoteRendererBusTitleMatchesRichDocument(t *testing.T) {
	tests := []struct {
		name string
		img  *Image
		want string
	}{
		{name: "route", img: testBusImage(), want: "校车 东区 → 西区"},
		{name: "all routes", img: testBusAllImage(), want: "校车"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (RemoteRenderer{}).buildBusRequest(tt.img).Title
			if got != tt.want {
				t.Fatalf("request title = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoteRendererUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // immediately unreachable

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want unavailable error", err)
	}
}

func TestRemoteRendererHonorsCanceledContext(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNGContext(ctx, testBusImage())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("canceled render made an HTTP request")
	}
}

func TestRemoteRendererRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxRemotePNGBytes+1))
	}))
	defer server.Close()

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want oversized-response error", err)
	}
}

func TestRemoteRendererRejectsMalformedPNG(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write([]byte("not a png"))
	}))
	defer server.Close()

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "decode rendered PNG header") {
		t.Fatalf("err = %v, want malformed-PNG error", err)
	}
}

func TestRemoteRendererRejectsTruncatedPNG(t *testing.T) {
	payload := testRemotePNG(t)
	// Keep the signature and IHDR, but remove the IDAT/IEND data. DecodeConfig
	// accepts this header; a complete decode must reject it.
	if len(payload) < 33 {
		t.Fatalf("test PNG unexpectedly short: %d bytes", len(payload))
	}
	truncated := payload[:33]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(truncated)
	}))
	defer server.Close()

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "decode rendered PNG") {
		t.Fatalf("err = %v, want truncated-PNG error", err)
	}
}

func TestRemoteRendererRejectsMismatchedDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "9")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(testRemotePNG(t))
	}))
	defer server.Close()

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "disagrees with PNG dimensions") {
		t.Fatalf("err = %v, want dimension-mismatch error", err)
	}
}

func TestRemoteRendererRejectsNonPNGImage(t *testing.T) {
	var payload bytes.Buffer
	if err := jpeg.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 8, 6)), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload.Bytes())
	}))
	defer server.Close()

	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	_, _, _, err := renderer.RenderPNG(testBusImage())
	if err == nil || !strings.Contains(err.Error(), "instead of PNG") {
		t.Fatalf("err = %v, want non-PNG error", err)
	}
}

func TestRemoteRendererRichPayload(t *testing.T) {
	payload := testRemotePNG(t)
	var gotRequest remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return time.Date(2026, 9, 2, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}
	img := NewRichTextImage("todo", `# 待办

## 本周待办
| 任务 | 截止 |
| --- | --- |
| 提交数据库实验报告 | 07-10 |
| 写完文献综述初稿 | 07-12 | ✨ |
| 还图书馆的书 | 07-15 |`, "待办：提交数据库实验报告等 3 项")
	if img == nil {
		t.Skip("nil image")
	}
	img.Ref = "4471"
	if _, _, _, err := renderer.RenderPNG(img); err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}

	if gotRequest.Kind != "rich" {
		t.Fatalf("request kind = %q, want rich", gotRequest.Kind)
	}
	var gotPayload remoteRichPayload
	if err := json.Unmarshal(gotRequest.Payload, &gotPayload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if gotPayload.Title != "待办" {
		t.Fatalf("payload title = %q", gotPayload.Title)
	}
	if len(gotPayload.Blocks) != 1 || gotPayload.Blocks[0].Table == nil {
		t.Fatalf("blocks = %#v, want a single table block", gotPayload.Blocks)
	}
	block := gotPayload.Blocks[0]
	if block.Heading != "本周待办" {
		t.Fatalf("block heading = %q", block.Heading)
	}
	table := block.Table
	if got := table.Header; len(got) != 2 || got[0] != "任务" || got[1] != "截止" {
		t.Fatalf("table header = %#v", got)
	}
	if got := table.HeaderEmphasis; len(got) != 2 || got[0] || got[1] {
		t.Fatalf("table header_emphasis = %#v, want [false false]", got)
	}
	if len(table.Rows) != 3 || !table.Rows[1].Highlight || table.Rows[0].Highlight || table.Rows[2].Highlight {
		t.Fatalf("table rows = %#v, want 3 rows with only row 1 highlighted", table.Rows)
	}
	if got := table.Rows[0].Cells; len(got) != 2 || got[0] != "提交数据库实验报告" || got[1] != "07-10" {
		t.Fatalf("first row cells = %#v", got)
	}
	if len(gotPayload.Footer) != 2 || gotPayload.Footer[0] != "2026-09-02（周三）13:00" ||
		gotPayload.Footer[1] != "#4471 · Life @ USTC" {
		t.Fatalf("footer = %#v", gotPayload.Footer)
	}
	if raw := string(gotRequest.Payload); strings.Contains(raw, "content_width") || strings.Contains(raw, "column_widths") || strings.Contains(raw, "wrapped") {
		t.Fatalf("payload contains obsolete geometry: %s", raw)
	}
}

func TestRemoteRendererRichTextWrap(t *testing.T) {
	var gotRequest remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(testRemotePNG(t))
	}))
	defer server.Close()

	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return time.Date(2026, 9, 2, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}
	img := NewRichTextImage("help", `# Bot 帮助

发送「帮助 课表」可以查看「课表」命令的具体用法。
直接发送「课表」即可查看今天的课程安排，发送「校车」查看校车时刻表。
`+strings.Repeat("这是一段用于触发自动换行的较长的说明文字，", 8), "Bot 帮助")
	if img == nil {
		t.Skip("nil image")
	}
	if _, _, _, err := renderer.RenderPNG(img); err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}

	if gotRequest.Kind != "rich" {
		t.Fatalf("request kind = %q, want rich", gotRequest.Kind)
	}
	var gotPayload remoteRichPayload
	if err := json.Unmarshal(gotRequest.Payload, &gotPayload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if gotPayload.Title != "Bot 帮助" {
		t.Fatalf("payload title = %q", gotPayload.Title)
	}
	if len(gotPayload.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(gotPayload.Blocks))
	}
	block := gotPayload.Blocks[0]
	if block.Table != nil {
		t.Fatalf("block = %#v, want a text block", block)
	}
	if len(block.Lines) != 3 {
		t.Fatalf("block lines = %d, want 3 source lines: %q", len(block.Lines), block.Lines)
	}
	if block.Lines[0] != "发送「帮助 课表」可以查看「课表」命令的具体用法。" {
		t.Fatalf("first line = %q", block.Lines[0])
	}
	if !strings.Contains(block.Lines[2], "这是一段用于触发自动换行的较长的说明文字") || strings.Contains(block.Lines[2], "…") {
		t.Fatalf("long source line was changed: %q", block.Lines[2])
	}
	if raw := string(gotRequest.Payload); strings.Contains(raw, "content_width") || strings.Contains(raw, "wrapped") {
		t.Fatalf("payload contains obsolete layout fields: %s", raw)
	}
}

func TestRemoteRendererRichTitleOnly(t *testing.T) {
	var gotRequest remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testRemotePNG(t))
	}))
	defer server.Close()

	img := NewRichTextImage("help", "# 标题", "标题")
	if img == nil {
		t.Fatal("title-only image is nil")
	}
	renderer := RemoteRenderer{Endpoint: server.URL + "/render"}
	if _, _, _, err := renderer.RenderPNG(img); err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	if gotRequest.Kind != "rich" {
		t.Fatalf("request kind = %q, want rich", gotRequest.Kind)
	}
	var payload remoteRichPayload
	if err := json.Unmarshal(gotRequest.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Title != "标题" {
		t.Fatalf("payload title = %q, want 标题", payload.Title)
	}
	if len(payload.Blocks) != 0 {
		t.Fatalf("payload blocks = %d, want 0", len(payload.Blocks))
	}
}

func TestRemoteRendererRejectsEmptyRichText(t *testing.T) {
	renderer := RemoteRenderer{Endpoint: "http://127.0.0.1:1/render"}
	_, _, _, err := renderer.RenderPNG(&Image{AltText: "text fallback"})
	if err == nil || !strings.Contains(err.Error(), "rich text is empty") {
		t.Fatalf("err = %v, want empty-rich-text error", err)
	}
}
