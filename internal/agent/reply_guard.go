package agent

import (
	"errors"
	"regexp"
	"strings"
)

const calendarURLGuardReply = "日历订阅链接只能由系统安全发送，这次没有发送。请稍后直接再问我获取链接。"

var (
	errUnverifiedCalendarURL = errors.New("calendar subscription URLs must come from the host command")
	replyURLPattern          = regexp.MustCompile(`(?i)\b(?:https?|webcal)://[^\s<>"']+`)
)

func hasCalendarSubscriptionURL(text string) bool {
	return len(calendarURLs(text)) > 0
}

func calendarURLs(text string) []string {
	matches := replyURLPattern.FindAllString(text, -1)
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		match = strings.TrimRight(match, ".,;:!?，。；：！？)]}）》」』")
		path := match
		if index := strings.IndexAny(path, "?#"); index >= 0 {
			path = path[:index]
		}
		lowerPath := strings.ToLower(path)
		if strings.HasSuffix(lowerPath, ".ics") ||
			strings.Contains(lowerPath, "/api/calendar-feeds/") ||
			strings.Contains(lowerPath, "/ical/") {
			urls = append(urls, match)
		}
	}
	return urls
}
