package commands

import (
	"context"
	"regexp"
	"strings"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

var roomCodePattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)

// Bare messages must follow USTC room numbering, so course codes and ordinary
// numbers do not activate a public room lookup. Exact availability comes from Life.
var bareRoomCodePattern = regexp.MustCompile(`^(?:1[1-3][0-9]{2}|2[1-8][0-9]{2}|5[1-5][0-9]{2}|3(?:[AB][1-5]|C[1-3])[0-9]{2}|GT-(?:A4|B[12]|C1)[0-9]{2}|GH-[1-4][0-9]{2}|G2-B[3-5][0-9]{2}|G3-[A-Z]?[0-9]{3,4}|GX-[A-Z]?[0-9]{3,4}|Z[0-9]{3}|ARTS[34][0-9]{2})$`)

var roomCodeInTextPattern = regexp.MustCompile(`[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*`)

func roomMapArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return len(args) <= 1
	}
	return len(args) == 1 && roomCodePattern.MatchString(strings.TrimSpace(args[0]))
}

func normalizeRoomMapArgs(args []string) []string {
	if len(args) != 1 || firstArgIsHelp(args) {
		return args
	}
	return []string{life.NormalizeRoomCode(args[0])}
}

func roomMapExecutor(h Handler, ctx context.Context, _ store.Identity, inv Invocation) CapabilityOutcome {
	code := ""
	if len(inv.Args) == 1 {
		code = life.NormalizeRoomCode(inv.Args[0])
	}
	if code == "" {
		return outcomeFromResponse(h, Response{
			Text: "请提供教室编号，例如：教室 3A204。",
			Kind: inv.Name,
		})
	}
	room, err := h.Life.RoomMap(ctx, code)
	if err != nil {
		return outcomeFromResponse(h, Response{Text: h.commandError("教室地图查不到：", err), Kind: inv.Name})
	}
	h.markData(map[string]any{"operation": "room_map", "room": room})
	return outcomeFromResponse(h, RoomMapResponse(room))
}

// RoomMapResponse turns the shared REST/MCP payload into the concise bot
// response. Highlighted maps are preferred; overview maps are still useful
// when an exact room annotation is not available.
func RoomMapResponse(room life.RoomMap) Response {
	code := strings.TrimSpace(room.Code)
	if code == "" {
		code = "该教室"
	}
	response := Response{
		Data: map[string]any{"operation": "room_map", "room": room},
		Kind: "room_map",
	}
	imageURL := strings.TrimSpace(room.ImageURL)
	if imageURL == "" {
		imageURL = strings.TrimSpace(room.SourceImageURL)
	}
	status := strings.ToLower(strings.TrimSpace(room.Status))
	if (status != "highlighted" && status != "overview") || imageURL == "" {
		response.Text = "未找到 " + code + " 的教室地图。"
		return response
	}
	title := "教室 " + code
	if status == "overview" {
		title += "（楼层/建筑概览，未确认该房间位置）"
	}
	response.Image = &responses.Image{
		Kind:  "room-map",
		Title: title,
		URL:   imageURL,
	}
	return response
}

func parseNaturalRoomIntent(raw string) ParseResult {
	normalized := life.NormalizeRoomCode(strings.TrimSpace(raw))
	compact := strings.Join(strings.Fields(normalized), "")
	compact = strings.Trim(compact, "，,。！？!?；;：:")
	if compact == "" || containsAny(compact, []string{"预约", "借用", "修改", "删除", "添加"}) {
		return ParseResult{Status: ParseStatusUnknown}
	}
	hasRoomIntent := containsAny(strings.ToLower(compact), []string{
		"教室", "房间", "地图", "位置", "在哪", "哪里", "怎么走",
		"room", "map", "where",
	})
	candidateText := strings.Trim(strings.TrimSpace(normalized), "，,。！？!?；;：:")
	if !hasRoomIntent && !bareRoomCodePattern.MatchString(candidateText) {
		return ParseResult{Status: ParseStatusUnknown}
	}
	matches := roomCodeInTextPattern.FindAllString(candidateText, -1)
	candidates := make([]string, 0, len(matches))
	for _, match := range matches {
		if roomCodeCandidate(match) {
			candidates = append(candidates, match)
		}
	}
	if len(candidates) != 1 {
		return ParseResult{Status: ParseStatusUnknown}
	}
	code := candidates[0]
	result := acceptedCommandResult(raw, string(CapabilityRoomMap), []string{life.NormalizeRoomCode(code)})
	result.Invocation.NaturalRoute = "room_map"
	return result
}

func roomCodeCandidate(value string) bool {
	hasLetter := strings.ContainsAny(strings.ToLower(value), "abcdefghijklmnopqrstuvwxyz")
	hasDigit := strings.ContainsAny(value, "0123456789")
	if hasLetter && hasDigit {
		return true
	}
	return !hasLetter && hasDigit && len(value) >= 3 && len(value) <= 5
}
