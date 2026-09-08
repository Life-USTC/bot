package main

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleServer(t *testing.T, width, height int) *httptest.Server {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
}

func TestRunRejectsCardWidthOutOfRange(t *testing.T) {
	server := exampleServer(t, 600, exampleMinHeight)
	defer server.Close()
	err := run(server.URL, t.TempDir(), "bus-single")
	if err == nil || !strings.Contains(err.Error(), "image width 600, want between 912 and 3852") {
		t.Fatalf("run error = %v, want a card-width regression failure", err)
	}
}

func TestRunRejectsEmptyLookingCard(t *testing.T) {
	// Cards are sized to their content, so only an implausibly short render
	// signals a regression.
	server := exampleServer(t, exampleMinWidth, 90)
	defer server.Close()
	err := run(server.URL, t.TempDir(), "bus-single")
	if err == nil || !strings.Contains(err.Error(), "image height 90, want at least 360") {
		t.Fatalf("run error = %v, want a card-height regression failure", err)
	}
}

func TestRunWritesEveryExampleAndGallery(t *testing.T) {
	server := exampleServer(t, exampleMinWidth, exampleMinHeight)
	defer server.Close()
	dir := t.TempDir()
	if err := run(server.URL, dir, ""); err != nil {
		t.Fatal(err)
	}
	gallery, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures() {
		filename := f.name + ".png"
		if _, err := os.Stat(filepath.Join(dir, filename)); err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(gallery), `src="`+filename+`"`) {
			t.Errorf("gallery is missing %s", filename)
		}
	}
}

func TestRunReportsSidecarFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "render unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := run(server.URL, t.TempDir(), "bus-single")
	if err == nil || !strings.Contains(err.Error(), "bus-single: render sidecar returned 503") {
		t.Fatalf("run error = %v, want fixture-specific sidecar failure", err)
	}
}

func TestRunRejectsEmptyFixtureSelection(t *testing.T) {
	if err := run("", t.TempDir(), "missing"); err == nil {
		t.Fatal("an empty fixture selection must fail verification")
	}
}

func TestWritePNGReportsOutputFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "card.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePNG(dir, "card.png", []byte("png")); err == nil {
		t.Fatal("unwritable output must fail verification")
	}
}
