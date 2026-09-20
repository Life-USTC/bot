// Bus semantics: the parts of a shuttle timetable that decide meaning rather
// than pixels — which trip is next, which have already departed, and which
// end of a route a card title names. renderd owns the layout; these answers
// still belong to the bot.

package responses

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type busRenderTable struct {
	Header         []string
	HeaderEmphasis []bool
	Rows           []busRenderRow
}

type busRenderRow struct {
	Cells     []string
	Highlight bool
	Departed  bool
}

func busRenderTitle(img *Image) string {
	title := strings.TrimSpace(img.Title)
	title = strings.TrimPrefix(title, "校车 ")
	if title == "校车" || title == "" {
		return "校车"
	}
	return title
}

func busNextWait(tables []busRenderTable, title string, now time.Time) (string, string) {
	now = now.In(time.FixedZone("CST", 8*60*60))
	nowMinutes := now.Hour()*60 + now.Minute()
	_, dest := parseBusEndpoints(title)
	best := -1
	for _, table := range tables {
		destCol := -1
		for i, h := range table.Header {
			if h == dest && dest != "" {
				destCol = i
				break
			}
		}
		for _, row := range table.Rows {
			cell := ""
			if destCol >= 0 && destCol < len(row.Cells) {
				cell = row.Cells[destCol]
			} else if len(row.Cells) > 0 {
				cell = row.Cells[0]
			}
			if cell == "" {
				continue
			}
			minutes, ok := parseBusClock(cell)
			if !ok || minutes < nowMinutes {
				continue
			}
			if best < 0 || minutes < best {
				best = minutes
			}
		}
	}
	if best < 0 {
		return "", ""
	}
	return formatBusClock(best), formatBusWait(best - nowMinutes)
}

func formatBusClock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func formatBusWait(minutes int) string {
	if minutes <= 0 {
		return "现在"
	}
	return strconv.Itoa(minutes) + " 分钟"
}

func parseBusClock(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
		return 0, false
	}
	hour, ok := parseTwoDigitNumber(parts[0])
	if !ok {
		return 0, false
	}
	minute, ok := parseTwoDigitNumber(parts[1])
	if !ok || hour > 23 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func parseTwoDigitNumber(value string) (int, bool) {
	if len(value) > 2 {
		return 0, false
	}
	out := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		out = out*10 + int(r-'0')
	}
	return out, true
}

func markBusRowsByTime(tables []busRenderTable, title string, now time.Time) {
	now = now.In(time.FixedZone("CST", 8*60*60))
	nowMinutes := now.Hour()*60 + now.Minute()
	_, dest := parseBusEndpoints(title)
	bestRoute := -1
	bestRow := -1
	bestArrival := -1
	for ti, table := range tables {
		destCol := -1
		for i, h := range table.Header {
			if h == dest && dest != "" {
				destCol = i
				break
			}
		}
		for ri := range table.Rows {
			if len(table.Rows[ri].Cells) == 0 {
				continue
			}
			minutes, ok := parseBusClock(table.Rows[ri].Cells[0])
			if !ok {
				continue
			}
			if minutes < nowMinutes {
				table.Rows[ri].Departed = true
				continue
			}
			arrival := minutes
			if destCol >= 0 && destCol < len(table.Rows[ri].Cells) {
				if arrivalMin, ok := parseBusClock(table.Rows[ri].Cells[destCol]); ok {
					arrival = arrivalMin
				}
			}
			if bestArrival < 0 || arrival < bestArrival {
				bestArrival = arrival
				bestRoute = ti
				bestRow = ri
			}
		}
	}
	if bestRoute >= 0 && bestRow >= 0 {
		tables[bestRoute].Rows[bestRow].Highlight = true
	}
}

func parseBusEndpoints(title string) (string, string) {
	title = strings.TrimSpace(title)
	prefixes := []string{"校车 · ", "校车 ", "校车", "到 "}
	for _, p := range prefixes {
		if strings.HasPrefix(title, p) {
			title = strings.TrimPrefix(title, p)
			break
		}
	}
	if title == "校车" || title == "" {
		return "", ""
	}
	for _, sep := range []string{"→", "-", "到"} {
		if parts := strings.SplitN(title, sep, 2); len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return title, ""
}

func richDocumentIsBus(doc richDocument) bool {
	title := strings.TrimSpace(doc.Title)
	return title == "校车" || strings.HasPrefix(title, "校车 ")
}
