package responses

import "strings"

// NewRoomMapImage uses the highlighted room image when available, otherwise
// the floor overview. An unavailable map has no image to deliver.
func NewRoomMapImage(code, status, imageURL, sourceImageURL string) *Image {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "highlighted" && status != "overview" {
		return nil
	}
	url := strings.TrimSpace(imageURL)
	if url == "" {
		url = strings.TrimSpace(sourceImageURL)
	}
	if url == "" {
		return nil
	}
	code = strings.TrimSpace(code)
	title := "教室 " + code
	if status == "overview" {
		title += "（楼层/建筑概览，未确认该房间位置）"
	}
	return &Image{Kind: "room-map", Title: title, URL: url}
}
