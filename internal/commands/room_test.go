package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestRoomMapCommandAndNaturalQueryArePublic(t *testing.T) {
	for _, test := range []struct {
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
		if result.Invocation.Policy().DataScope != DataScopePublic {
			t.Errorf("ParseCommand(%q) scope = %q, want public", test.input, result.Invocation.Policy().DataScope)
		}
	}
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

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), EnableImageResponses: false}
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

func TestRoomMapResponseDoesNotAttachImageForUnavailableRoom(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{Code: "GT-Z999", Status: "unavailable"})
	if response.Text != "未找到 GT-Z999 的教室地图。" || response.Image != nil {
		t.Fatalf("unavailable response = %#v", response)
	}
}

func TestRoomMapOverviewPreservesStatusWithoutText(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{
		Code: "3A299", Building: "三教", Floor: "2", Status: "overview",
		SourceImageURL: "https://static.example/floors/3-2.png",
	})
	if response.Text != "" || response.Data.(map[string]any)["room"].(life.RoomMap).Status != "overview" {
		t.Fatalf("overview response = %#v", response)
	}
	if response.Image == nil || response.Image.URL != "https://static.example/floors/3-2.png" {
		t.Fatalf("overview image = %#v", response.Image)
	}
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

func TestBareRoomLookupDoesNotCaptureOtherMessages(t *testing.T) {
	for _, input := range []string{"2026", "12345", "CS1001", "MATH1001", "520", "52010", "A5201", "5201A", "5 201", "G3-hello", "GX-news", "Zoom", "5201 5202", "明天在5201上课", "预约5201", "课程 5201"} {
		result := ParseCommand(input)
		if result.Valid() && result.Invocation.ID() == CapabilityRoomMap {
			t.Errorf("non-room command routed as room: %q", input)
		}
	}
}
