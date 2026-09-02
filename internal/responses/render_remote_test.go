package responses

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
	"testing"
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
		w.Write(payload)
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
	if gotPayload.Title != "校车 · 东区 → 西区" {
		t.Fatalf("request title = %q", gotPayload.Title)
	}
	if len(gotPayload.Tables) != 2 {
		t.Fatalf("request tables = %d, want 2", len(gotPayload.Tables))
	}
	if got := gotPayload.Tables[0].Header; len(got) != 4 || got[0] != "东区" || got[3] != "高新区" {
		t.Fatalf("first table header = %#v", got)
	}
	if got := gotPayload.Tables[0].ColumnWidths; len(got) != 4 {
		t.Fatalf("first table column widths = %#v", got)
	}
	if len(gotPayload.RowsOfTables) != 2 || len(gotPayload.RowsOfTables[0]) != 1 || gotPayload.RowsOfTables[0][0] != 0 || gotPayload.RowsOfTables[1][0] != 1 {
		t.Fatalf("rows_of_tables = %#v, want [[0] [1]]", gotPayload.RowsOfTables)
	}
	if gotPayload.ContentWidth <= 0 {
		t.Fatalf("content_width = %d, want > 0", gotPayload.ContentWidth)
	}
	if len(gotPayload.Footer) != 2 || gotPayload.Footer[1] != "Life @ USTC" {
		t.Fatalf("footer = %#v", gotPayload.Footer)
	}
	if gotPayload.NextTime == "" {
		t.Fatalf("request next_time empty, want next bus hint")
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

func TestRemoteRendererRejectsNonBusKind(t *testing.T) {
	renderer := RemoteRenderer{Endpoint: "http://127.0.0.1:1/render"}
	img := NewTextImage("todo", "待办", "写报告")
	if img == nil {
		t.Skip("nil image")
	}
	if _, _, _, err := renderer.RenderPNG(img); err == nil || !strings.Contains(err.Error(), "only supports") {
		t.Fatalf("err = %v, want unsupported-kind error", err)
	}
}
