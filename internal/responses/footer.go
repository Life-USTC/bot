// Card footer: the two right-aligned caption lines every card ends with.

package responses

import (
	"strings"
	"time"
)

// richFooterLines builds the two right-aligned caption lines every card ends
// with: when the card was produced, and where it came from. The request
// number identifies the outbound record, so a user reporting a bad card can
// quote it and an operator can find that exact delivery.
func richFooterLines(now time.Time, ref string) [2]string {
	source := "Life @ USTC"
	if ref = strings.TrimSpace(ref); ref != "" {
		source = "#" + ref + " · " + source
	}
	return [2]string{footerStampLine(now), source}
}

func footerStampLine(now time.Time) string {
	return now.Format("2006-01-02") + "（" + weekdayLabel(now) + "）" + now.Format("15:04")
}

var weekdayLabels = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

func weekdayLabel(now time.Time) string {
	return weekdayLabels[int(now.Weekday())]
}
