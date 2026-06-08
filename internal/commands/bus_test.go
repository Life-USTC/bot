package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestHandleGroupOnlyAllowsBusKeywords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bus" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"routes":[
				{"id":1,"stops":[
					{"campus":{"nameCn":"东区"}},
					{"campus":{"nameCn":"北区"}},
					{"campus":{"nameCn":"西区"}}
				]}
			],
			"trips":[
				{"routeId":1,"dayType":"weekday","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]},
				{"routeId":1,"dayType":"weekend","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]}
			]
		}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	groupInput := Input{
		Text: "东区到西区校车还有吗",
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "group",
			ConversationID:   "100",
		},
	}
	reply, ok := handler.Handle(context.Background(), groupInput)
	if !ok {
		t.Fatal("group bus message was not handled")
	}
	if !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿  →  北区\u3000 ———  →  西区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("reply = %q", reply)
	}

	groupInput.Identity.ConversationType = " GROUP "
	reply, ok = handler.Handle(context.Background(), groupInput)
	if !ok {
		t.Fatal("padded/cased group bus message was not handled")
	}

	groupInput.Text = "/life td"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group personal command reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,summary=&#91;动画表情&#93;,file=1.png,sub_type=1,url=https://example.invalid/download?rkey=CAQSMJSxCxAi3h4QEhInHuJOdWi5QXU7]"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group image reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,file=1.png] 校车"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if !ok || !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("group image caption reply = %q, ok = %v", reply, ok)
	}
}

func TestBusAtReturnsNoServiceAfterLastTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bus" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"西区"}}]}],
			"trips":[{"routeId":1,"dayType":"weekday","departureTime":"09:00","departureMinutes":540,"arrivalTime":"09:15"}]
		}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, lifedata.ChinaLocation())
	if reply := handler.busAt(context.Background(), nil, now); reply != "今天后面没查到校车。" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNextBusItemsFiltersRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "北区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:00", "departureMinutes": float64(540), "arrivalTime": "09:15"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:20" || items[0].Route != "东区 → 北区 → 西区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsUsesShanghaiTime(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "08:30", "departureMinutes": float64(510), "arrivalTime": "08:45"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
		},
	}
	now := time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC)
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:20" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsSupportsDestinationOnlyFilter(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"到", "东区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "西区 → 东区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsSkipsInvalidDepartureMinutes(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureMinutes": float64(560.5), "arrivalTime": "09:35"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:30" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsIgnoresBlankRouteIDs(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": "   ",
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "南区"}},
					map[string]any{"campus": map[string]any{"nameCn": "高新区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{
				"routeId":          "missing-route",
				"dayType":          "weekday",
				"departureTime":    "09:20",
				"departureMinutes": float64(560),
				"arrivalTime":      "09:35",
				"stopTimes": []any{
					map[string]any{"campusName": "东区", "time": "09:20"},
					map[string]any{"campusName": "西区", "time": "09:35"},
				},
			},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "东区 → 西区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusByRouteReturnsOneTripPerRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:10", "departureMinutes": float64(550), "arrivalTime": "09:25"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "10:10", "departureMinutes": float64(610), "arrivalTime": "10:25"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "东区 → 西区" || items[0].DepartureTime != "09:10" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].Route != "西区 → 东区" || items[1].DepartureTime != "09:30" {
		t.Fatalf("second item = %#v", items[1])
	}
}

func TestNextBusByRouteSortsByDepartureCampus(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:05", "departureMinutes": float64(545), "arrivalTime": "09:20"},
			map[string]any{
				"routeId":          float64(1),
				"dayType":          "weekday",
				"departureTime":    "09:30",
				"departureMinutes": float64(570),
				"arrivalTime":      "09:45",
				"stopTimes": []any{
					map[string]any{"campusName": "东区", "time": "09:30"},
					map[string]any{"campusName": "北区"},
					map[string]any{"campusName": "西区", "time": "09:45"},
				},
			},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureCampus != "东区" || items[1].DepartureCampus != "西区" {
		t.Fatalf("items = %#v", items)
	}
	lines := formatBusItemsByDepartureCampus(items, 8)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区\u3000 𝟶𝟿:𝟹𝟶  →  北区\u3000 ———  →  西区\u3000 𝟶𝟿:𝟺𝟻\n\n西区\u3000 𝟶𝟿:𝟶𝟻") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestFormatBusItemsNoLimitShowsAllRoutes(t *testing.T) {
	items := []busItem{
		{DepartureCampus: "东区", Stops: []busStop{{Name: "东区", Time: "09:00"}}},
		{DepartureCampus: "西区", Stops: []busStop{{Name: "西区", Time: "09:05"}}},
	}

	lines := formatBusItemsByDepartureCampus(items, 0)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区") || !strings.Contains(got, "西区") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestBusArgsFromTextAcceptsEnglishCampusAliases(t *testing.T) {
	tests := map[string][]string{
		"Any BUS from EAST campus to west campus?": {"东区", "西区"},
		"bus from gx to north":                     {"高新区", "北区"},
		"bus to west campus":                       {"到", "西区"},
		"校车到西区":                                    {"到", "西区"},
		"bus to northeast tomorrow":                {},
		"bus from northeast to north":              {"北区"},
	}
	for text, want := range tests {
		got := busArgsFromText(text)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, got, want)
		}
	}
}

func TestBusArgsFromTextAvoidsOneCharCampusInsideWords(t *testing.T) {
	tests := map[string][]string{
		"校车中午到西区": {"到", "西区"},
		"校车东到西":   {"东区", "西区"},
		"xc 东 西":  {"东区", "西区"},
	}
	for text, want := range tests {
		got := busArgsFromText(text)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, got, want)
		}
	}
}

func TestBusArgsFromTextKeepsRepeatedCampusEndpoints(t *testing.T) {
	tests := map[string][]string{
		"东区到东区校车":               {"东区", "东区"},
		"bus from east to east": {"东区", "东区"},
	}
	for text, want := range tests {
		got := busArgsFromText(text)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, got, want)
		}
	}
}
