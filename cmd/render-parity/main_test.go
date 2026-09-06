package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsSidecarFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "render unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := run(server.URL, t.TempDir(), "bus-single")
	if err == nil || !strings.Contains(err.Error(), "bus-single remote: render sidecar returned 503") {
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
