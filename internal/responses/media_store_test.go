package responses

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMediaStoreServesPNGBeforeTTL(t *testing.T) {
	store := NewMediaStore("https://bot.example/media", time.Minute)
	url, err := store.PutPNG([]byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(url, "https://bot.example")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	store.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string([]byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("body = %#v", body)
	}
}

func TestMediaStoreExpiresPNG(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	store := NewMediaStoreWithClock("https://bot.example/media", time.Second, func() time.Time { return now })
	url, err := store.PutPNG([]byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	path := strings.TrimPrefix(url, "https://bot.example")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	store.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMediaStoreReusesURLForIdenticalPNG(t *testing.T) {
	store := NewMediaStore("https://bot.example/media", time.Minute)
	first, err := store.PutPNG([]byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutPNG([]byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identical PNG URLs differ: %q != %q", first, second)
	}
}
