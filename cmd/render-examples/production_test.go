package main

import (
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Life-USTC/Bot/internal/responses"
)

func TestBusSnapshotsReachRendererWithoutLosingTripsOrEmptyStops(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func() *responses.Image
		tables int
		trips  int
	}{
		{"east-west", busSingleImage, 4, 44},
		{"weekday", busAllImage, 10, 105},
		{"saturday", busWeekendImage, 10, 49},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request struct {
				Payload struct {
					Tables []struct {
						Header []string `json:"header"`
						Rows   []struct {
							Cells     []string `json:"cells"`
							Highlight bool     `json:"highlight"`
						} `json:"rows"`
					} `json:"tables"`
				} `json:"payload"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "image/png")
				if err := png.Encode(w, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			renderer := responses.RemoteRenderer{Endpoint: server.URL, Now: fixtureNow}
			if _, _, _, err := renderer.RenderPNG(tc.build()); err != nil {
				t.Fatal(err)
			}
			if len(request.Payload.Tables) != tc.tables {
				t.Fatalf("tables = %d, want %d", len(request.Payload.Tables), tc.tables)
			}
			trips, highlights := 0, 0
			foundNorthStop := false
			foundHighTechToEast := false
			foundEastToHighTech := false
			for _, table := range request.Payload.Tables {
				trips += len(table.Rows)
				for _, row := range table.Rows {
					if len(row.Cells) != len(table.Header) {
						t.Errorf("row width %d != header width %d", len(row.Cells), len(table.Header))
					}
					if row.Highlight {
						highlights++
					}
				}
				if reflect.DeepEqual(table.Header, []string{"东区", "北区", "西区"}) {
					foundNorthStop = true
					if !reflect.DeepEqual(table.Rows[0].Cells, []string{"07:30", "", "07:40"}) {
						t.Errorf("unknown North Campus time shifted the West Campus arrival: %v", table.Rows[0].Cells)
					}
				}
				if reflect.DeepEqual(table.Header, []string{"高新区", "先研院", "西区", "东区"}) {
					foundHighTechToEast = true
					if len(table.Rows) == 0 || len(table.Rows[0].Cells) != 4 || table.Rows[0].Cells[2] != "" {
						t.Errorf("unknown West Campus time was interpolated on route 7: %v", table.Rows)
					}
					if tc.name == "east-west" && !reflect.DeepEqual(table.Rows[0].Cells, []string{"06:40", "06:45", "", "07:25"}) {
						t.Errorf("weekday route 7 cells = %v", table.Rows[0].Cells)
					}
				}
				if reflect.DeepEqual(table.Header, []string{"东区", "西区", "先研院", "高新区"}) {
					foundEastToHighTech = true
					if len(table.Rows) == 0 || len(table.Rows[0].Cells) != 4 || table.Rows[0].Cells[2] != "" {
						t.Errorf("unknown Advanced Institute time was interpolated on route 8: %v", table.Rows)
					}
					if tc.name == "east-west" && !reflect.DeepEqual(table.Rows[0].Cells, []string{"06:50", "07:00", "", "07:40"}) {
						t.Errorf("weekday route 8 cells = %v", table.Rows[0].Cells)
					}
				}
			}
			if trips != tc.trips || highlights != 1 || !foundNorthStop ||
				(tc.name == "east-west" && (!foundHighTechToEast || !foundEastToHighTech)) {
				t.Errorf("trips=%d highlights=%d north-stop=%v hightech-east=%v east-hightech=%v", trips, highlights, foundNorthStop, foundHighTechToEast, foundEastToHighTech)
			}
		})
	}
}

func TestPublicCourseSampleKeepsOfficialPeriodsAndConsistentDayView(t *testing.T) {
	week, day := gridWeekImage().Grid, gridDayImage().Grid
	if len(week.Periods) != 13 || week.Periods[0].Time != "07:50–08:35" || week.Periods[12].Time != "21:10–21:55" {
		t.Fatalf("incomplete official timetable: %v", week.Periods)
	}
	if len(week.Items) != 11 || len(day.Items) != 3 {
		t.Fatalf("missing public section meetings: week=%d day=%d", len(week.Items), len(day.Items))
	}
	occupied := map[[2]int]bool{}
	var wednesday []responses.ScheduleGridItem
	for _, item := range week.Items {
		for p := item.StartPeriod; p <= item.EndPeriod; p++ {
			key := [2]int{item.Day, p}
			if occupied[key] {
				t.Errorf("sample sections conflict at day %d period %d", item.Day, p)
			}
			occupied[key] = true
		}
		if item.Day == 3 {
			item.Day = 0
			wednesday = append(wednesday, item)
		}
	}
	if !reflect.DeepEqual(day.Items, wednesday) || !reflect.DeepEqual(day.Periods, week.Periods) {
		t.Fatal("daily sample differs from the corresponding weekly column")
	}
}
