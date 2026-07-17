package responses

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
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
	if len(store.items) != 0 || len(store.ids) != 1 {
		t.Fatalf("expired media was not removed: items=%d ids=%d", len(store.items), len(store.ids))
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

func TestMediaStoreReusesImageURLForSameBusinessContentOnSameDay(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewMediaStoreWithClock("https://bot.example/media", time.Hour, func() time.Time { return now })
	image := NewTextImage("help", "Bot 帮助", "Bot 帮助\n发送「帮助 课表」查看具体用法")

	first, err := store.PutImagePNG(image, []byte("rendered at 08:00"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	second, err := store.PutImagePNG(image, []byte("rendered at 08:10"))
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatalf("same business image URLs differ: %q != %q", first, second)
	}
	req := httptest.NewRequest(http.MethodGet, strings.TrimPrefix(second, "https://bot.example"), nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "rendered at 08:10" {
		t.Fatalf("served data = %q, want latest rendering", got)
	}
}

func TestMediaStoreReusesImageURLAfterPNGBytesExpire(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewMediaStoreWithClock("https://bot.example/media", time.Minute, func() time.Time { return now })
	image := NewTextImage("help", "Bot 帮助", "Bot 帮助")
	first, err := store.PutImagePNG(image, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	second, err := store.PutImagePNG(image, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("semantic URL changed after byte TTL: %q != %q", first, second)
	}
}

func TestMediaStoreChangesImageURLWhenContentOrDateChanges(t *testing.T) {
	now := time.Date(2026, 7, 17, 23, 59, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewMediaStoreWithClock("https://bot.example/media", time.Minute, func() time.Time { return now })
	firstImage := NewScheduleGridImage("schedule", "本周课表", &ScheduleGrid{
		Days:    []ScheduleGridDay{{Label: "周一", Date: "07-13"}},
		Periods: []ScheduleGridPeriod{{Label: "第1节", Time: "08:00"}},
		Items:   []ScheduleGridItem{{Day: 0, StartPeriod: 0, EndPeriod: 0, Course: "数学分析"}},
	}, "本周课表")

	first, err := store.PutImagePNG(firstImage, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	changedGrid := NewScheduleGridImage("schedule", "本周课表", &ScheduleGrid{
		Days:    []ScheduleGridDay{{Label: "周一", Date: "07-13"}},
		Periods: []ScheduleGridPeriod{{Label: "第1节", Time: "08:00"}},
		Items:   []ScheduleGridItem{{Day: 0, StartPeriod: 0, EndPeriod: 0, Course: "线性代数"}},
	}, "本周课表")
	changed, err := store.PutImagePNG(changedGrid, []byte("changed"))
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatalf("business content change reused URL %q", changed)
	}

	now = now.Add(2 * time.Minute)
	nextDay, err := store.PutImagePNG(firstImage, []byte("next day"))
	if err != nil {
		t.Fatal(err)
	}
	if nextDay == first {
		t.Fatalf("date change reused URL %q", nextDay)
	}
}

func TestMediaStoreImageURLDoesNotExposeContentHash(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMediaStoreWithClock("https://bot.example/media", time.Minute, func() time.Time { return now })
	image := NewTextImage("help", "Bot 帮助", "Bot 帮助")
	url, err := store.PutImagePNG(image, []byte("png bytes"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := image.cacheKey(now)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(path.Base(url), ".png"); got == key {
		t.Fatalf("URL exposes image cache hash: %q", url)
	}
}

func TestMediaStorePutCleansExpiredEntries(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMediaStoreWithClock("https://bot.example/media", time.Minute, func() time.Time { return now })
	if _, err := store.PutPNG([]byte("first")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(mediaIDTTL + time.Minute)
	if _, err := store.PutPNG([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || len(store.ids) != 1 {
		t.Fatalf("expired media accumulated: items=%d ids=%d", len(store.items), len(store.ids))
	}
}

func TestMediaStoreBoundsStableURLMappings(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store := NewMediaStoreWithClock("https://bot.example/media", time.Minute, func() time.Time { return now })
	var firstURL string
	for i := 0; i <= mediaIDLimit; i++ {
		url, err := store.PutPNG([]byte(fmt.Sprintf("image-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstURL = url
		}
		now = now.Add(time.Millisecond)
	}
	if len(store.ids) != mediaIDLimit {
		t.Fatalf("stable URL mappings = %d, want %d", len(store.ids), mediaIDLimit)
	}
	req := httptest.NewRequest(http.MethodGet, strings.TrimPrefix(firstURL, "https://bot.example"), nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("evicted mapping removed live PNG: status = %d", rec.Code)
	}
}
