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

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), EnableImageResponses: true}
	response, ok := handler.HandleResponse(context.Background(), Input{
		Text:     "教室 3A204",
		Identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "group", ConversationID: "42"},
	})
	if !ok {
		t.Fatal("room command was not handled")
	}
	if response.Text != "3A204：三教 2" || response.Image == nil || response.Image.Kind != "room-map" || response.Image.URL != "https://static.example/rooms/3A204.png" {
		t.Fatalf("room response = %#v", response)
	}
}

func TestRoomMapResponseDoesNotAttachImageForUnavailableRoom(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{Code: "GT-Z999", Status: "unavailable"}, true)
	if response.Text != "未找到 GT-Z999 的教室地图。" || response.Image != nil {
		t.Fatalf("unavailable response = %#v", response)
	}
}

func TestRoomMapOverviewExplainsThatRoomPositionIsUnverified(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{
		Code: "3A299", Building: "三教", Floor: "2", Status: "overview",
		SourceImageURL: "https://static.example/floors/3-2.png",
	}, true)
	if !strings.Contains(response.Text, "楼层/建筑概览") || !strings.Contains(response.Text, "未确认该房间位置") {
		t.Fatalf("overview response = %#v", response)
	}
	if response.Image == nil || response.Image.URL != "https://static.example/floors/3-2.png" {
		t.Fatalf("overview image = %#v", response.Image)
	}
}

func TestRoomMapResponseIncludesURLWhenImagesAreDisabled(t *testing.T) {
	response := RoomMapResponse(life.RoomMap{
		Code: "3A204", Building: "三教", Floor: "2", Status: "highlighted",
		ImageURL: "https://static.example/rooms/3A204.png",
	}, false)
	if response.Image != nil || !strings.Contains(response.Text, "地图：https://static.example/rooms/3A204.png") {
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
