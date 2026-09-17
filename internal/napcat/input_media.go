package napcat

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/message"
)

// Called during off-websocket enrichment. File IDs are resolved through
// get_file, which downloads via the QQ client itself; the get_*_file_url
// actions depend on NapCat's packet backend, which does not exist on every
// platform (macOS has none). A co-located NapCat answers get_file with a
// local path; the bridge forwards it only when the operator opted in, and
// the attachment parser applies the same opt-in before reading it.
func (b *Bridge) resolveEventMedia(ctx context.Context, event *messageEvent) {
	media := event.inbound().Media
	for index, item := range media {
		if item.Kind != message.InputMediaFile || item.URL != "" || item.FileID == "" {
			continue
		}
		response, err := b.callNapCatAction(ctx, "get_file", map[string]any{"file_id": item.FileID})
		if err != nil {
			b.logf("input file resolve failed: file_id=%q: %v", item.FileID, err)
			continue
		}
		var result struct {
			File string `json:"file"`
			URL  string `json:"url"`
			Name string `json:"file_name"`
			Size string `json:"file_size"`
		}
		if err := json.Unmarshal(response.Data, &result); err != nil {
			b.logf("input file resolve returned invalid data: file_id=%q: %v", item.FileID, err)
			continue
		}
		switch url, file := strings.TrimSpace(result.URL), strings.TrimSpace(result.File); {
		case isInputMediaURL(url):
			media[index].URL = url
		case b.AllowLocalMediaPaths && strings.HasPrefix(file, "/"):
			media[index].URL = file
		default:
			b.logf("input file resolve returned no usable URL: file_id=%q", item.FileID)
			continue
		}
		if media[index].Name == "" {
			media[index].Name = result.Name
		}
		if media[index].Size == 0 {
			if size, err := strconv.ParseInt(strings.TrimSpace(result.Size), 10, 64); err == nil {
				media[index].Size = size
			}
		}
	}
	event.resolvedMedia = media
}
