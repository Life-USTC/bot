package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/specification"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func TestSpecRoomMapCommandAndNaturalQueryArePublic(t *testing.T) {
	t.Run("room-map.bot-recognition", func(t *testing.T) {
		contract := specification.Begin(t)
		for caseIndex, test := range []struct {
			input string
			want  []string
		}{
			{input: "5201", want: []string{"5201"}},
			{input: "３ａ２０４？", want: []string{"3A204"}},
			{input: "gt-b110", want: []string{"GT-B110"}},
			{input: "1101", want: []string{"1101"}},
			{input: "2103", want: []string{"2103"}},
			{input: "3C201", want: []string{"3C201"}},
			{input: "GH-104", want: []string{"GH-104"}},
			{input: "G2-B302", want: []string{"G2-B302"}},
			{input: "GX-C1001", want: []string{"GX-C1001"}},
			{input: "G3-101", want: []string{"G3-101"}},
			{input: "Z101", want: []string{"Z101"}},
			{input: "ARTS401", want: []string{"ARTS401"}},
			{input: "教室 3A204", want: []string{"3A204"}},
			{input: "教室 ３ａ２０４", want: []string{"3A204"}},
			{input: "请帮我查一下 3a204 的地图", want: []string{"3A204"}},
			{input: "3A204 在哪里？", want: []string{"3A204"}},
			{input: "1101 在哪里？", want: []string{"1101"}},
			{input: "where is 3A204?", want: []string{"3A204"}},
			{input: "３ａ２０４ 在哪？", want: []string{"3A204"}},
		} {
			result := ParseCommand(test.input)
			if !result.Valid() || result.Invocation.ID() != CapabilityRoomMap || strings.Join(result.Invocation.Args, " ") != strings.Join(test.want, " ") {
				t.Errorf("ParseCommand(%q) = %#v, want room map %v", test.input, result, test.want)
				continue
			}
			contract.Check(fmt.Sprintf("input-%02d", caseIndex+1), specification.RoomInput{Input: test.input, RoomCommand: result.Valid() && result.Invocation.ID() == CapabilityRoomMap, Code: strings.Join(result.Invocation.Args, " "), Scope: string(result.Invocation.Policy().DataScope)})
		}

	})
}

func TestRoomMapCommandDeliversHighlightedImageInGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/rooms/3A204/map" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected room request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(life.RoomMap{
			Code: "3A204", Building: "三教", Floor: "2", Status: "highlighted",
			ImageURL: "https://static.example/rooms/3A204.png", SourceImageURL: "https://static.example/floors/3-2.png",
		})
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	response, ok := handler.HandleResponse(context.Background(), Input{
		Text:     "３ａ２０４",
		Identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "group", ConversationID: "42"},
	})
	if !ok {
		t.Fatal("room command was not handled")
	}
	if response.Text != "" || response.Image == nil || response.Image.Kind != "room-map" || response.Image.URL != "https://static.example/rooms/3A204.png" {
		t.Fatalf("room response = %#v", response)
	}
	data, ok := response.Data.(map[string]any)
	if !ok || data["operation"] != "room_map" {
		t.Fatalf("room Data = %#v", response.Data)
	}
	room, ok := data["room"].(life.RoomMap)
	if !ok || room.Code != "3A204" {
		t.Fatalf("room domain Data = %#v", data["room"])
	}
}

func TestSpecRoomMapResponseDoesNotAttachImageForUnavailableRoom(t *testing.T) {
	t.Run("room-map.bot-unavailable", func(t *testing.T) {
		contract := specification.Begin(t)
		response := RoomMapResponse(life.RoomMap{Code: "GT-Z999", Status: "unavailable"})
		room := response.Data.(map[string]any)["room"].(life.RoomMap)
		images := 0
		imageURL := ""
		if response.Image != nil {
			images = 1
			imageURL = response.Image.URL
		}
		contract.Check("unavailable", specification.RoomPresentation{Text: response.Text, Code: room.Code, Status: room.Status, Images: images, ImageURL: imageURL, Requests: []string{}})

	})
}

func TestSpecRoomMapOverviewPreservesStatusWithoutText(t *testing.T) {
	t.Run("room-map.bot-overview", func(t *testing.T) {
		contract := specification.Begin(t)
		response := RoomMapResponse(life.RoomMap{
			Code: "3A299", Building: "三教", Floor: "2", Status: "overview",
			SourceImageURL: "https://static.example/floors/3-2.png",
		})
		if response.Text != "" || response.Data.(map[string]any)["room"].(life.RoomMap).Status != "overview" {
			t.Fatalf("overview response = %#v", response)
		}
		room := response.Data.(map[string]any)["room"].(life.RoomMap)
		images := 0
		imageURL := ""
		if response.Image != nil {
			images = 1
			imageURL = response.Image.URL
		}
		contract.Check("overview", specification.RoomPresentation{Text: response.Text, Code: room.Code, Floor: room.Floor, Status: room.Status, Images: images, ImageURL: imageURL, Requests: []string{}})

	})
}

func TestRoomMapResponseMissingImageDoesNotFallBackToLocation(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{
		Code: "3A204", Building: "三教", Floor: "2", Status: "highlighted",
		ImageURL: "",
	})
	if response.Image != nil || response.Text != "未找到 3A204 的教室地图。" {
		t.Fatalf("disabled image response = %#v", response)
	}
}

func TestGenericCourseQueryDoesNotBecomeRoomLookup(t *testing.T) {
	for _, input := range []string{"查询 CS1001", "查一下 MATH1001"} {
		result := ParseCommand(input)
		if result.Valid() && result.Invocation.ID() == CapabilityRoomMap {
			t.Fatalf("generic catalog query routed as room: %s", input)
		}
	}
}

func TestSpecBareRoomLookupDoesNotCaptureOtherMessages(t *testing.T) {
	t.Run("room-map.bot-avoid-false-positive", func(t *testing.T) {
		contract := specification.Begin(t)
		for caseIndex, input := range []string{"2026", "12345", "CS1001", "MATH1001", "520", "52010", "A5201", "5201A", "5 201", "G3-hello", "GX-news", "Zoom", "5201 5202", "明天在5201上课", "预约5201", "课程 5201"} {
			result := ParseCommand(input)
			contract.Check(fmt.Sprintf("input-%02d", caseIndex+1), specification.RoomInput{Input: input, RoomCommand: result.Valid() && result.Invocation.ID() == CapabilityRoomMap})
		}

	})
}

func TestSpecAgendaDoesNotAttachRoomMaps(t *testing.T) {
	t.Run("room-map.bot-no-unsolicited-attachments", func(t *testing.T) {
		contract := specification.Begin(t)
		withAgendaRand(t, 0.99)
		paths := []string{}
		wire := contract.Wire("get-api-workspace-calendar-events")
		fixture := wire.FixtureFile("calendar.json")
		wire.Fixture(fixture)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			wire.Request(r)
			if r.URL.Path != "/api/workspace/calendar/events" {
				t.Errorf("unsolicited request: %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(fixture)
		}))
		defer server.Close()
		identity := testIdentity()
		handler := testAuthedHandler(t, server, identity)
		response, ok := handler.HandleResponse(t.Context(), Input{Text: "日程 2026-09-18", Identity: identity})
		if !ok {
			t.Fatal("agenda not handled")
		}
		if response.Image != nil && response.Image.Kind == "room-map" {
			t.Fatal("agenda attached an unrequested room map")
		}
		images := 0
		if response.Image != nil && response.Image.Kind == "room-map" {
			images = 1
		}
		contract.Check("agenda", specification.RoomPresentation{RoomText: strings.Contains(textutil.PlainMonospace(response.Text), "3A101"), Images: images, Requests: paths})
	})
}
