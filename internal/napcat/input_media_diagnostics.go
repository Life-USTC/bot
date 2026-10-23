package napcat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/message"
)

// Observe the same structured branches as inputPartsFromValue before source
// types are lost. CQ's unknown types produce text markers, not unknown media.
// Log bounded segment types and allowlisted field shapes, never media values.
func (b *Bridge) logUnknownMessageMedia(value any, source string, depth int, remaining *int) {
	if b.Logger == nil || depth > maxForwardDepth || *remaining <= 0 {
		return
	}
	for _, raw := range messageSegments(value) {
		if *remaining <= 0 {
			return
		}
		segment, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		data, _ := segment["data"].(map[string]any)
		layout := "data"
		if data == nil {
			data = segment
			layout = "flat"
		}
		switch typ {
		case "forward", "node":
			nested := firstPresent(data["content"], data["message"], segment["content"], segment["message"])
			b.logUnknownForwardMedia(nested, source+".forward", depth+1, remaining)
		case "text", "reply", "image", "mface", "face", "file", "record", "video":
			continue
		default:
			media := inputMediaFromNapCatSegment(typ, data)
			if media.Kind != message.InputMediaUnknown || (media.URL == "" && media.FileID == "") {
				continue
			}
			*remaining--
			b.logf("napcat input media observation: source=%s branch=unknown-type layout=%s segment=%s kind=unknown fields=[%s]",
				source, layout, diagnosticSegmentType(typ), diagnosticMediaFields(data))
		}
	}
}

func (b *Bridge) logUnknownForwardMedia(payload any, source string, depth int, remaining *int) {
	if b.Logger == nil || depth > maxForwardDepth || *remaining <= 0 {
		return
	}
	for _, raw := range forwardPayloadNodes(payload) {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data, _ := node["data"].(map[string]any)
		if data == nil {
			data = node
		}
		content := firstPresent(data["content"], data["message"], node["message"], node["content"])
		b.logUnknownMessageMedia(content, source, depth+1, remaining)
		if *remaining <= 0 {
			return
		}
	}
}

func diagnosticSegmentType(value string) string {
	if value == "" {
		return "empty"
	}
	if len(value) > 48 {
		return "invalid"
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return "invalid"
		}
	}
	return value
}

func diagnosticMediaFields(data map[string]any) string {
	var fields []string
	for _, key := range []string{"content_type", "mime_type", "type", "name", "filename", "file", "url", "path", "file_id", "emoji_id", "id", "size", "file_size", "bytes"} {
		if value, ok := data[key]; ok {
			fields = append(fields, key+"="+diagnosticJSONType(value))
		}
	}
	return strings.Join(fields, ",")
}

func diagnosticJSONType(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		if strings.TrimSpace(typed) == "" {
			return "empty-string"
		}
		return "string"
	case bool:
		return "boolean"
	case int, int64, float64, json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any, []map[string]any:
		return "array"
	default:
		return "other"
	}
}
