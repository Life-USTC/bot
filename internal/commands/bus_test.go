package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

const busPreferenceTestData = `{
	"campuses":[
		{"id":1,"nameCn":"东区","namePrimary":"东区"},
		{"id":2,"nameCn":"西区","namePrimary":"西区"},
		{"id":3,"nameCn":"南区","namePrimary":"南区"}
	],
	"routes":[
		{"id":1,"stops":[
			{"campus":{"nameCn":"东区"}},
			{"campus":{"nameCn":"西区"}}
		]},
		{"id":2,"stops":[
			{"campus":{"nameCn":"南区"}},
			{"campus":{"nameCn":"东区"}}
		]}
	],
	"trips":[
		{"routeId":1,"dayType":"weekday","departureTime":"23:00","departureMinutes":1380,"arrivalTime":"23:20","stopTimes":[
			{"campusName":"东区","time":"23:00"},
			{"campusName":"西区","time":"23:20"}
		]},
		{"routeId":2,"dayType":"weekday","departureTime":"23:10","departureMinutes":1390,"arrivalTime":"23:30","stopTimes":[
			{"campusName":"南区","time":"23:10"},
			{"campusName":"东区","time":"23:30"}
		]}
	]
}`

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
	if !strings.Contains(reply, "东区 \t北区 \t西区 \n𝟸𝟹:𝟻𝟿\t　　 \t𝟸𝟹:𝟻𝟿") || strings.Contains(reply, "———") {
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
	if !ok || !strings.Contains(reply, "东区 \t北区 \t西区 \n𝟸𝟹:𝟻𝟿\t　　 \t𝟸𝟹:𝟻𝟿") {
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
	if reply := handler.busAt(context.Background(), store.Identity{}, nil, now); reply != "今天后面没查到校车。" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBusPreferencesViewAndSet(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var saved map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(busPreferenceTestData))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			if got := r.Header.Get("Authorization"); got != "Bearer access" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":1,"preferredDestinationCampusId":2,"showDepartedTrips":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/bus/preferences":
			if got := r.Header.Get("Authorization"); got != "Bearer access" {
				t.Fatalf("authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":3,"preferredDestinationCampusId":1,"showDepartedTrips":true}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "校车偏好", Identity: ident})
	if !ok {
		t.Fatal("preference command was not handled")
	}
	if !strings.Contains(reply, "路线：东区 → 西区") || !strings.Contains(reply, "已发车：不显示") {
		t.Fatalf("reply = %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "校车 设置 南区 东区 已发车 开", Identity: ident})
	if !ok {
		t.Fatal("set preference command was not handled")
	}
	if saved["preferredOriginCampusId"] != float64(3) ||
		saved["preferredDestinationCampusId"] != float64(1) ||
		saved["showDepartedTrips"] != true {
		t.Fatalf("saved = %#v", saved)
	}
	if !strings.Contains(reply, "已更新校车偏好：") ||
		!strings.Contains(reply, "路线：南区 → 东区") ||
		!strings.Contains(reply, "已发车：显示") ||
		!strings.Contains(reply, "南区：不显示") {
		t.Fatalf("reply = %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "校车 设置 南区 开", Identity: ident})
	if !ok {
		t.Fatal("set south preference command was not handled")
	}
	if !strings.Contains(reply, "南区：显示") {
		t.Fatalf("reply = %q", reply)
	}
	settings, err := handler.Store.BusSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.ShowSouthCampus {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestHandleBusBarePrivateQueryHidesSouthByDefault(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(busPreferenceTestData))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":3,"preferredDestinationCampusId":1,"showDepartedTrips":true}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, nil, time.Date(2026, 6, 2, 23, 5, 0, 0, lifedata.ChinaLocation()))
	if reply != "今天后面没查到校车。" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBusBarePrivateQueryCanShowSouth(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(busPreferenceTestData))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":3,"preferredDestinationCampusId":1,"showDepartedTrips":true}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	if err := handler.Store.SaveBusSettings(ctx, store.BusSettings{Identity: ident, ShowSouthCampus: true}); err != nil {
		t.Fatal(err)
	}
	reply := handler.busAt(ctx, ident, nil, time.Date(2026, 6, 2, 23, 5, 0, 0, lifedata.ChinaLocation()))
	if !strings.Contains(reply, "南区 \t东区 \n𝟸𝟹:𝟷𝟶\t𝟸𝟹:𝟹𝟶") || strings.Contains(reply, "𝟸𝟹:𝟶𝟶") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBusExplicitSouthRouteIgnoresSouthPreference(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(busPreferenceTestData))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"showDepartedTrips":false}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, []string{"南区", "东区"}, time.Date(2026, 6, 2, 22, 0, 0, 0, lifedata.ChinaLocation()))
	if !strings.Contains(reply, "南区 \t东区 \n𝟸𝟹:𝟷𝟶\t𝟸𝟹:𝟹𝟶") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBusPreferredRouteUsesSavedPreferences(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(busPreferenceTestData))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"preferredOriginCampusId":3,"preferredDestinationCampusId":1,"showDepartedTrips":false}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, []string{"我的路线"}, time.Date(2026, 6, 2, 22, 0, 0, 0, lifedata.ChinaLocation()))
	if !strings.Contains(reply, "南区 \t东区 \n𝟸𝟹:𝟷𝟶\t𝟸𝟹:𝟹𝟶") || strings.Contains(reply, "𝟸𝟹:𝟶𝟶") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBusExplicitRouteShowsAllMatchingTripsWithPreference(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(`{
				"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"高新区"}}]}],
				"trips":[
					{"routeId":1,"dayType":"weekday","departureTime":"08:00","departureMinutes":480,"arrivalTime":"08:40","stopTimes":[
						{"campusName":"东区","time":"08:00"},{"campusName":"高新区","time":"08:40"}
					]},
					{"routeId":1,"dayType":"weekday","departureTime":"09:00","departureMinutes":540,"arrivalTime":"09:40","stopTimes":[
						{"campusName":"东区","time":"09:00"},{"campusName":"高新区","time":"09:40"}
					]},
					{"routeId":1,"dayType":"weekday","departureTime":"10:00","departureMinutes":600,"arrivalTime":"10:40","stopTimes":[
						{"campusName":"东区","time":"10:00"},{"campusName":"高新区","time":"10:40"}
					]}
				]
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"showDepartedTrips":false}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, []string{"东区", "高新区"}, time.Date(2026, 6, 2, 8, 30, 0, 0, lifedata.ChinaLocation()))
	if strings.Contains(reply, "𝟶𝟾:𝟶𝟶") || !strings.Contains(reply, "𝟶𝟿:𝟶𝟶") || !strings.Contains(reply, "𝟷𝟶:𝟶𝟶") {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(reply, "东区  \t高新区") ||
		!strings.Contains(reply, "𝟶𝟿:𝟶𝟶 \t𝟶𝟿:𝟺𝟶 ") ||
		!strings.Contains(reply, "𝟷𝟶:𝟶𝟶 \t𝟷𝟶:𝟺𝟶 ") ||
		strings.Contains(reply, "→") {
		t.Fatalf("reply is not a stop-time table: %q", reply)
	}
}

func TestHandleBusExplicitRouteCanShowDepartedTripsFromPreference(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(`{
				"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"高新区"}}]}],
				"trips":[
					{"routeId":1,"dayType":"weekday","departureTime":"08:00","departureMinutes":480,"arrivalTime":"08:40","stopTimes":[
						{"campusName":"东区","time":"08:00"},{"campusName":"高新区","time":"08:40"}
					]},
					{"routeId":1,"dayType":"weekday","departureTime":"09:00","departureMinutes":540,"arrivalTime":"09:40","stopTimes":[
						{"campusName":"东区","time":"09:00"},{"campusName":"高新区","time":"09:40"}
					]}
				]
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"showDepartedTrips":true}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, []string{"东区", "高新区"}, time.Date(2026, 6, 2, 8, 30, 0, 0, lifedata.ChinaLocation()))
	if !strings.Contains(reply, "𝟶𝟾:𝟶𝟶") || !strings.Contains(reply, "𝟶𝟿:𝟶𝟶") {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(reply, "东区  \t高新区") ||
		!strings.Contains(reply, "𝟶𝟾:𝟶𝟶 \t𝟶𝟾:𝟺𝟶 ") ||
		!strings.Contains(reply, "𝟶𝟿:𝟶𝟶 \t𝟶𝟿:𝟺𝟶 \t✨") {
		t.Fatalf("reply is not a stop-time table: %q", reply)
	}
}

func TestHandleBusExplicitRouteSplitsRouteVariants(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus":
			_, _ = w.Write([]byte(`{
				"routes":[
					{"id":"direct","stops":[
						{"campus":{"nameCn":"东区"}},
						{"campus":{"nameCn":"西区"}}
					]},
					{"id":"via-north","stops":[
						{"campus":{"nameCn":"东区"}},
						{"campus":{"nameCn":"北区"}},
						{"campus":{"nameCn":"西区"}}
					]}
				],
				"trips":[
					{"routeId":"via-north","dayType":"weekday","departureTime":"09:20","departureMinutes":560,"arrivalTime":"09:35","stopTimes":[
						{"campusName":"东区","time":"09:20"},{"campusName":"北区"},{"campusName":"西区","time":"09:35"}
					]},
					{"routeId":"direct","dayType":"weekday","departureTime":"09:30","departureMinutes":570,"arrivalTime":"09:40","stopTimes":[
						{"campusName":"东区","time":"09:30"},{"campusName":"西区","time":"09:40"}
					]}
				]
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/bus/preferences":
			_, _ = w.Write([]byte(`{"preference":{"showDepartedTrips":false}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.busAt(ctx, ident, []string{"东区", "西区"}, time.Date(2026, 6, 2, 9, 0, 0, 0, lifedata.ChinaLocation()))
	want := strings.Join([]string{
		"东区 \t西区 ",
		"𝟶𝟿:𝟹𝟶\t𝟶𝟿:𝟺𝟶",
		"",
		"东区 \t北区 \t西区 ",
		"𝟶𝟿:𝟸𝟶\t　　 \t𝟶𝟿:𝟹𝟻",
	}, "\n")
	if reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
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

func TestBusQueryArgsSupportsAfterTime(t *testing.T) {
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, lifedata.ChinaLocation())
	args, options := busQueryArgs([]string{"高新区", "东区", "after", "09:25"}, now)
	if strings.Join(args, " ") != "高新区 东区" {
		t.Fatalf("args = %#v", args)
	}
	if options.Now.IsZero() || options.Now.Hour() != 9 || options.Now.Minute() != 25 {
		t.Fatalf("options = %#v", options)
	}
	if !options.After {
		t.Fatalf("options.After = false")
	}
	args, options = busQueryArgs([]string{"高新区", "东区", "after", "2026-06-09", "09:25"}, now)
	if strings.Join(args, " ") != "高新区 东区" || !options.After || options.Now.Day() != 9 || options.Now.Hour() != 9 || options.Now.Minute() != 25 {
		t.Fatalf("date args = %#v options = %#v", args, options)
	}
}

func TestNextBusItemsUsesAfterTimeOption(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "高新区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "06:40", "departureMinutes": float64(400), "arrivalTime": "07:25"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:40", "departureMinutes": float64(580), "arrivalTime": "10:25"},
		},
	}
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, lifedata.ChinaLocation())
	after := time.Date(2026, 6, 2, 9, 25, 0, 0, lifedata.ChinaLocation())
	items := nextBusItemsWithOptions(data, []string{"高新区", "东区"}, now, busQueryOptions{Now: after})
	if len(items) != 1 || items[0].DepartureTime != "09:40" {
		t.Fatalf("items = %#v", items)
	}
	items = nextBusItemsWithOptions(data, []string{"高新区", "东区"}, now, busQueryOptions{Now: after, ShowDeparted: true, After: true})
	if len(items) != 1 || items[0].DepartureTime != "09:40" {
		t.Fatalf("show departed with after items = %#v", items)
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

func TestNextBusItemsByRouteLimitReturnsMultipleTripsPerRoute(t *testing.T) {
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
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "11:10", "departureMinutes": float64(670), "arrivalTime": "11:25"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "12:10", "departureMinutes": float64(730), "arrivalTime": "12:25"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItemsByRouteLimitWithOptions(data, nil, now, busQueryOptions{}, 3)
	if len(items) != 4 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:10" || items[1].DepartureTime != "10:10" || items[2].DepartureTime != "11:10" {
		t.Fatalf("east-west items = %#v", items)
	}
	if strings.Contains(strings.Join([]string{items[0].DepartureTime, items[1].DepartureTime, items[2].DepartureTime}, " "), "12:10") {
		t.Fatalf("limit did not apply: %#v", items)
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
	lines := formatBusItemsByRouteGroup(items, 8)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区 \t北区 \t西区 \n𝟶𝟿:𝟹𝟶\t　　 \t𝟶𝟿:𝟺𝟻") ||
		!strings.Contains(got, "西区 \t东区 \n𝟶𝟿:𝟶𝟻\t𝟶𝟿:𝟸𝟶") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestFormatBusItemsGroupsByRouteKind(t *testing.T) {
	items := []busItem{
		{
			DepartureCampus:  "南区",
			DepartureMinutes: 720,
			Stops:            []busStop{{Name: "南区", Time: "12:00"}, {Name: "东区", Time: "12:15"}},
		},
		{
			DepartureCampus:  "东区",
			DepartureMinutes: 570,
			Stops:            []busStop{{Name: "东区", Time: "09:30"}, {Name: "北区"}, {Name: "西区", Time: "09:45"}},
		},
		{
			DepartureCampus:  "高新区",
			DepartureMinutes: 575,
			Stops:            []busStop{{Name: "高新区", Time: "09:35"}, {Name: "先研院", Time: "09:40"}, {Name: "东区", Time: "10:20"}},
		},
		{
			DepartureCampus:  "先研院",
			DepartureMinutes: 800,
			Stops:            []busStop{{Name: "先研院", Time: "13:20"}, {Name: "高新区", Time: "13:30"}},
		},
		{
			DepartureCampus:  "北区",
			DepartureMinutes: 840,
			Stops:            []busStop{{Name: "北区", Time: "14:00"}, {Name: "中区", Time: "14:10"}},
		},
	}

	got := strings.Join(formatBusItemsByRouteGroup(items, 0), "\n")
	eastHigh := "高新区\t先研院\t东区"
	eastWest := "东区 \t北区 \t西区"
	highTechLocal := "先研院\t高新区"
	south := "南区 \t东区"
	other := "北区 \t中区"
	for _, want := range []string{eastHigh, eastWest, highTechLocal, south, other} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted lines missing %q: %q", want, got)
		}
	}
	if !(strings.Index(got, eastHigh) < strings.Index(got, eastWest) &&
		strings.Index(got, eastWest) < strings.Index(got, highTechLocal) &&
		strings.Index(got, highTechLocal) < strings.Index(got, south) &&
		strings.Index(got, south) < strings.Index(got, other)) {
		t.Fatalf("formatted lines not grouped in route order: %q", got)
	}
}

func TestFormatBusItemsNoLimitShowsAllRoutes(t *testing.T) {
	items := []busItem{
		{DepartureCampus: "东区", Stops: []busStop{{Name: "东区", Time: "09:00"}}},
		{DepartureCampus: "西区", Stops: []busStop{{Name: "西区", Time: "09:05"}}},
	}

	lines := formatBusItemsByRouteGroup(items, 0)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区") || !strings.Contains(got, "西区") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestFormatBusItemsByRouteGroupUsesRouteTables(t *testing.T) {
	items := []busItem{
		{
			RouteID:          "east-west-local",
			DepartureMinutes: 570,
			Stops:            []busStop{{Name: "东区", Time: "09:30"}, {Name: "北区"}, {Name: "西区", Time: "09:45"}},
		},
		{
			RouteID:          "east-west-direct",
			DepartureMinutes: 550,
			Stops:            []busStop{{Name: "东区", Time: "09:10"}, {Name: "西区", Time: "09:25"}},
		},
		{
			RouteID:          "west-east",
			DepartureMinutes: 560,
			Stops:            []busStop{{Name: "西区", Time: "09:20"}, {Name: "东区", Time: "09:35"}},
		},
		{
			RouteID:          "east-west-direct",
			DepartureMinutes: 610,
			Stops:            []busStop{{Name: "东区", Time: "10:10"}, {Name: "西区", Time: "10:25"}},
		},
	}

	got := strings.Join(formatBusItemsByRouteGroup(items, 0), "\n")
	want := strings.Join([]string{
		"东区 \t西区 ",
		"𝟶𝟿:𝟷𝟶\t𝟶𝟿:𝟸𝟻",
		"𝟷𝟶:𝟷𝟶\t𝟷𝟶:𝟸𝟻",
		"",
		"东区 \t北区 \t西区 ",
		"𝟶𝟿:𝟹𝟶\t　　 \t𝟶𝟿:𝟺𝟻",
		"",
		"西区 \t东区 ",
		"𝟶𝟿:𝟸𝟶\t𝟶𝟿:𝟹𝟻",
	}, "\n")
	if got != want {
		t.Fatalf("formatted lines = %q, want %q", got, want)
	}
}

func TestFormatBusItemsAsStopTimeTablePadsCellsBeforeTabs(t *testing.T) {
	lines := formatBusItemsAsStopTimeTable([]busItem{{
		Stops: []busStop{
			{Name: "高新区", Time: "09:00"},
			{Name: "先研院", Time: "09:05"},
			{Name: "西区", Time: "09:10"},
			{Name: "东区", Time: "09:20"},
		},
	}})
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "高新区\t先研院\t西区  \t东区  ") {
		t.Fatalf("formatted table = %q", got)
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
