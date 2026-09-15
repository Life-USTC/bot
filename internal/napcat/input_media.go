package napcat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Life-USTC/Bot/internal/message"
)

// Called during off-websocket enrichment. Only platform-provided file IDs are
// resolved; local NapCat filesystem paths never become Bot download paths.
func (b *Bridge) resolveEventMedia(ctx context.Context, event *messageEvent) {
	media := event.inbound().Media
	for index, item := range media {
		if item.Kind != message.InputMediaFile || item.URL != "" || item.FileID == "" {
			continue
		}
		action := "get_private_file_url"
		params := map[string]any{"file_id": item.FileID}
		if isGroupMessageType(event.MessageType) {
			action = "get_group_file_url"
			params["group_id"] = fmt.Sprint(event.GroupID)
		}
		response, err := b.callNapCatAction(ctx, action, params)
		if err != nil {
			b.logf("input file URL unavailable")
			continue
		}
		var result struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(response.Data, &result) == nil && isInputMediaURL(result.URL) {
			media[index].URL = result.URL
		}
	}
	event.resolvedMedia = media
}
