package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
)

func TestAcademicWeekContextKeepsSundayStartForMidweekSemester(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/semesters/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"nameCn":"2026年秋季学期","startDate":"2026-09-01T00:00:00+08:00","endDate":"2027-01-17T23:59:59+08:00"}`))
	}))
	defer server.Close()
	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	for _, tc := range []struct {
		week int
		want string
	}{
		{1, "2026-08-30"},
		{2, "2026-09-06"},
	} {
		start, semester, err := handler.academicWeekContext(context.Background(), tc.week)
		if err != nil {
			t.Fatal(err)
		}
		if got := start.In(lifedata.ChinaLocation()).Format("2006-01-02 15:04:05"); got != tc.want+" 00:00:00" {
			t.Fatalf("week %d starts at %s, want %s midnight", tc.week, got, tc.want)
		}
		if got := lifedata.FirstString(semester, "nameCn"); got != "2026年秋季学期" {
			t.Fatalf("semester = %q", got)
		}
	}
}

func TestScheduleGridWeekMetadataKeepsPartiallyOverlappingWeeks(t *testing.T) {
	semester := map[string]any{
		"nameCn":    "2026年秋季学期",
		"startDate": "2026-09-01T00:00:00+08:00",
		"endDate":   "2026-09-07T23:59:59+08:00",
	}
	for _, tc := range []struct {
		start, dateRange, week string
	}{
		{"2026-08-30", "08/30-09/05", "第 1 周"},
		{"2026-09-06", "09/06-09/12", "第 2 周"},
		{"2026-09-13", "09/13-09/19", ""},
	} {
		t.Run(tc.start, func(t *testing.T) {
			start, err := time.ParseInLocation("2006-01-02", tc.start, lifedata.ChinaLocation())
			if err != nil {
				t.Fatal(err)
			}
			metadata := (Handler{}).scheduleGridWeekMetadata(context.Background(), start, start.AddDate(0, 0, 6), semester)
			wantSemester := "2026 秋季学期"
			if tc.week == "" {
				wantSemester = ""
			}
			if metadata.week != tc.week || metadata.semester != wantSemester || metadata.dateRange != tc.dateRange {
				t.Fatalf("metadata = %#v, want %q / %q / %q", metadata, tc.week, wantSemester, tc.dateRange)
			}
		})
	}
}

func TestAggregateSemesterSchedulesPreservesServerWeeksAndDateFallback(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, lifedata.ChinaLocation())
	end := time.Date(2026, 9, 7, 0, 0, 0, 0, lifedata.ChinaLocation())
	for _, tc := range []struct {
		name, date string
		weekIndex  any
		wantWeek   int
	}{
		{"missing week uses Shanghai Sunday", "2026-09-05T16:00:00Z", nil, 2},
		{"zero week uses date", "2026-09-06T08:00:00+08:00", 0, 2},
		{"negative week uses date", "2026-09-06T08:00:00+08:00", -1, 2},
		{"positive server week wins", "2026-09-06T08:00:00+08:00", 9, 9},
		{"semester first day included", "2026-08-31T16:00:00Z", 0, 1},
		{"semester final day included", "2026-09-07T15:59:59Z", 0, 2},
		{"before semester excluded", "2026-08-31T15:59:59Z", 9, 0},
		{"after semester excluded", "2026-09-07T16:00:00Z", 9, 0},
		{"undated server week kept", "", 9, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{
				"date": tc.date, "weekday": 1, "startTime": "08:00", "endTime": "09:35",
				"section": map[string]any{"id": 42, "course": map[string]any{"namePrimary": "Calculus"}},
			}
			if tc.weekIndex != nil {
				row["weekIndex"] = tc.weekIndex
			}
			entries := aggregateSemesterSchedules([]map[string]any{row}, start, end)
			if tc.wantWeek == 0 {
				if len(entries) != 0 {
					t.Fatalf("outside-semester entries = %#v", entries)
				}
				return
			}
			if len(entries) != 1 || len(entries[0].weeks) != 1 || !entries[0].weeks[tc.wantWeek] {
				t.Fatalf("entries = %#v, want one entry in week %d", entries, tc.wantWeek)
			}
		})
	}
}
