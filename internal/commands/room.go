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
	return outcomeFromResponse(h, RoomMapResponse(room, h.EnableImageResponses))
}

// RoomMapResponse turns the shared REST/MCP payload into the concise bot
// response. Highlighted maps are preferred; overview maps are still useful
// when an exact room annotation is not available.
func RoomMapResponse(room life.RoomMap, includeImage bool) Response {
	code := strings.TrimSpace(room.Code)
	if code == "" {
		code = "该教室"
	}
	location := strings.Join(nonEmptyRoomParts(room.Building, room.Floor), " ")
	text := code
	switch strings.ToLower(strings.TrimSpace(room.Status)) {
	case "highlighted":
		if location != "" {
			text += "：" + location
		}
	case "overview":
		if location != "" {
			text += "：" + location
		}
		text += "（楼层/建筑概览，未确认该房间位置）"
	default:
		return Response{Text: "未找到 " + code + " 的教室地图。", Kind: "room_map"}
	}

	response := Response{Text: text, Kind: "room_map"}
	imageURL := strings.TrimSpace(room.ImageURL)
	if imageURL == "" {
		imageURL = strings.TrimSpace(room.SourceImageURL)
	}
	if imageURL == "" {
		return response
	}
	if !includeImage {
		response.Text += "\n地图：" + imageURL
		return response
	}
	response.Image = &responses.Image{
		Kind:    "room-map",
		Title:   "教室 " + code,
		AltText: text,
		URL:     imageURL,
	}
	return response
}

func nonEmptyRoomParts(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseNaturalRoomIntent(raw string) ParseResult {
	normalized := life.NormalizeRoomCode(strings.TrimSpace(raw))
	compact := strings.Join(strings.Fields(normalized), "")
	compact = strings.Trim(compact, "，,。！？!?；;：:")
	if compact == "" || containsAny(compact, []string{"预约", "借用", "修改", "删除", "添加"}) {
		return ParseResult{Status: ParseStatusUnknown}
	}
	hasRoomIntent := containsAny(strings.ToLower(compact), []string{
		"教室", "房间", "地图", "位置", "在哪", "哪里", "怎么走", "查询", "查一下", "查",
		"room", "map", "where",
	})
	if !hasRoomIntent {
		return ParseResult{Status: ParseStatusUnknown}
	}
	candidateText := strings.Trim(strings.TrimSpace(normalized), "，,。！？!?；;：:")
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
