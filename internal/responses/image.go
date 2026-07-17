package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Image struct {
	Kind     string
	Title    string
	Lines    []string
	RichText string
	AltText  string
	URL      string
	Grid     *ScheduleGrid
}

type ScheduleGrid struct {
	Days    []ScheduleGridDay
	Periods []ScheduleGridPeriod
	Items   []ScheduleGridItem
}

type ScheduleGridDay struct {
	Label string
	Date  string
}

type ScheduleGridPeriod struct {
	Label string
	Time  string
}

type ScheduleGridItem struct {
	Day         int
	StartPeriod int
	EndPeriod   int
	Course      string
	Location    string
}

func (img *Image) cacheKey(now time.Time) (string, error) {
	if img == nil {
		return "", errors.New("response image is nil")
	}
	content := struct {
		Date     string
		Kind     string
		Title    string
		Lines    []string
		RichText string
		AltText  string
		Grid     *ScheduleGrid
	}{
		Date:     now.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02"),
		Kind:     img.Kind,
		Title:    img.Title,
		Lines:    img.Lines,
		RichText: img.RichText,
		AltText:  img.AltText,
		Grid:     img.Grid,
	}
	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func NewRichTextImage(kind, text, altText string) *Image {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if strings.TrimSpace(altText) == "" {
		altText = text
	}
	doc := parseRichText(text)
	return &Image{
		Kind:     strings.TrimSpace(kind),
		Title:    doc.Title,
		Lines:    strings.Split(strings.TrimSpace(altText), "\n"),
		RichText: text,
		AltText:  strings.TrimSpace(altText),
	}
}

func NewTextImage(kind, title, text string) *Image {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return &Image{
		Kind:     strings.TrimSpace(kind),
		Title:    strings.TrimSpace(title),
		Lines:    lines,
		RichText: legacyRichText(kind, title, lines),
		AltText:  text,
	}
}

func NewScheduleGridImage(kind, title string, grid *ScheduleGrid, altText string) *Image {
	title = strings.TrimSpace(title)
	altText = strings.TrimSpace(altText)
	if grid == nil || len(grid.Days) == 0 || len(grid.Periods) == 0 || altText == "" {
		return nil
	}
	return &Image{
		Kind:     strings.TrimSpace(kind),
		Title:    title,
		RichText: "# " + title,
		AltText:  altText,
		Lines:    strings.Split(altText, "\n"),
		Grid:     grid,
	}
}

func legacyRichText(kind, title string, lines []string) string {
	title = strings.TrimSpace(title)
	body := append([]string(nil), lines...)
	if len(body) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(body[0]), "："), ":")
		if first == title {
			body = body[1:]
		}
	}
	if strings.TrimSpace(kind) == "bus" {
		body = markdownTableLines(body)
	}
	return strings.TrimSpace("# " + title + "\n\n" + strings.Join(body, "\n"))
}

func markdownTableLines(lines []string) []string {
	out := []string{}
	atHeader := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			atHeader = true
			continue
		}
		cells := strings.Fields(line)
		if len(cells) == 0 {
			continue
		}
		out = append(out, "| "+strings.Join(cells, " | ")+" |")
		if atHeader {
			separators := make([]string, len(cells))
			for i := range separators {
				separators[i] = "---"
			}
			out = append(out, "| "+strings.Join(separators, " | ")+" |")
			atHeader = false
		}
	}
	return out
}

func (img *Image) WithURL(url string) *Image {
	if img == nil {
		return nil
	}
	next := *img
	next.URL = strings.TrimSpace(url)
	return &next
}
