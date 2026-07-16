package commands

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Response struct {
	Text  string
	Image *responses.Image
	Kind  string
}

func textResponse(text string) Response {
	return Response{Text: text}
}

func (h Handler) imageResponseFor(cmd parsedCommand, text string) *responses.Image {
	if !h.EnableImageResponses || strings.TrimSpace(text) == "" {
		return nil
	}
	if !successfulImageText(text) {
		return nil
	}
	imageText := imageRenderText(text)
	switch cmd.Name {
	case "schedule":
		if firstArgIs(cmd.Args, "help") {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		title := imageTitle(plainText, "课表")
		return responses.NewRichTextImage("schedule", scheduleRichText(title, plainText), imageText)
	case "todo":
		if !todoImageArgs(cmd.Args) {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		return richTextImage("todo", imageTitle(plainText, "待办"), plainText)
	case "homework":
		if !homeworkImageArgs(cmd.Args) {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		return richTextImage("homework", imageTitle(plainText, "作业"), plainText)
	case "section_homeworks":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("homework", imageTitle(plainText, "作业"), plainText)
	case "exam", "section_exams":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("exam", imageTitle(plainText, "考试"), plainText)
	case "nextclass":
		plainText := textutil.PlainMonospace(text)
		title := imageTitle(plainText, "下一节课")
		return responses.NewRichTextImage("nextclass", scheduleRichText(title, plainText), imageText)
	case "overview":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("overview", imageTitle(plainText, "今日安排"), plainText)
	case "dashboard":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("dashboard", imageTitle(plainText, "我的概览"), plainText)
	case "upcoming_deadlines":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("deadlines", imageTitle(plainText, "近期截止"), plainText)
	case "bus":
		if firstArgIs(cmd.Args, "help") || busPreferenceArgs(cmd.Args) {
			return nil
		}
		title := "校车"
		body := busImageRenderText(text)
		return responses.NewRichTextImage("bus", busRichText(title, body, parseBusRouteArgs(cmd.Args)), body)
	default:
		return nil
	}
}

func scheduleRichText(title, text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(lines[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			lines = lines[1:]
		}
	}

	out := []string{"# " + strings.TrimSpace(title), ""}
	atSectionStart := true
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			inTable = false
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			heading := strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(heading))
			atSectionStart = false
			inTable = false
			continue
		}
		if cells, ok := scheduleRichTableCells(line); ok {
			if !inTable {
				out = append(out,
					markdownRichTableRow([]string{"校区", "教室", "时间", "课程"}),
					markdownRichTableRow([]string{"---", "---", "---", "---"}),
				)
			}
			out = append(out, markdownRichTableRow(cells))
			inTable = true
		} else {
			out = append(out, imageRenderText(line))
			inTable = false
		}
		atSectionStart = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func scheduleRichTableCells(line string) ([]string, bool) {
	columns := strings.Split(line, "\t")
	if len(columns) < 3 {
		return nil, false
	}
	place := strings.TrimSpace(columns[0])
	timeRange := strings.TrimSpace(columns[1])
	course := strings.TrimSpace(strings.Join(columns[2:], " "))
	if timeRange == "" || course == "" {
		return nil, false
	}
	campus, room := scheduleCampusAndRoom(place)
	return []string{campus, room, timeRange, course}, true
}

func scheduleCampusAndRoom(place string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(place))
	if len(parts) == 0 {
		return "", ""
	}
	campus := campusName(parts[0])
	switch campus {
	case "东区", "西区", "中区", "北区", "南区", "高新区", "先研院":
		return campus, strings.TrimSpace(strings.TrimPrefix(place, parts[0]))
	default:
		return "", strings.TrimSpace(place)
	}
}

func markdownRichTableRow(cells []string) string {
	trimmed := make([]string, len(cells))
	for i, cell := range cells {
		trimmed[i] = strings.TrimSpace(cell)
	}
	return "| " + strings.Join(trimmed, " | ") + " |"
}

func richTextImage(kind, title, text string) *responses.Image {
	text = textutil.PlainMonospace(text)
	body := strings.Split(strings.TrimSpace(text), "\n")
	if len(body) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(body[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			body = body[1:]
		}
	}
	body = richTextSectionLines(body)
	body = richTextTableSections(kind, body)
	richText := strings.TrimSpace("# " + strings.TrimSpace(title) + "\n\n" + strings.Join(body, "\n"))
	return responses.NewRichTextImage(kind, richText, imageRenderText(text))
}

func richTextTableSections(kind string, lines []string) []string {
	out := []string{}
	section := ""
	if kind == "todo" {
		section = "待办"
	}
	block := []string{}
	flush := func() {
		if table, ok := richImageTable(kind, section, block); ok {
			out = append(out, table...)
		} else {
			out = append(out, block...)
		}
		block = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, trimmed)
			section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			continue
		}
		block = append(block, line)
	}
	flush()
	return out
}

func richImageTable(kind, section string, lines []string) ([]string, bool) {
	data := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			data = append(data, line)
		}
	}
	if len(data) == 0 {
		return nil, false
	}

	var headers []string
	var cellsFor func(string) ([]string, bool)
	sectionKind := richImageSectionKind(section)
	if sectionKind == "" && (kind == "homework" || kind == "exam") {
		sectionKind = kind
	}
	switch sectionKind {
	case "schedule":
		headers = []string{"校区", "教室", "时间", "课程"}
		cellsFor = overviewScheduleRichTableCells
	case "todo":
		headers = []string{"#", "截止", "待办"}
		cellsFor = todoRichTableCells
	case "homework":
		headers = []string{"#", "截止", "课程", "作业"}
		cellsFor = homeworkRichTableCells
	case "exam":
		headers = []string{"#", "日期", "时间", "课程", "教学班", "方式", "教室"}
		cellsFor = examRichTableCells
	default:
		return nil, false
	}
	rows := make([][]string, 0, len(data))
	for _, line := range data {
		cells, ok := cellsFor(line)
		if !ok {
			cells, ok = richImageMoreRow(line, len(headers))
		}
		if !ok {
			return nil, false
		}
		rows = append(rows, cells)
	}

	out := []string{markdownRichTableRow(headers)}
	separators := make([]string, len(headers))
	for i := range separators {
		separators[i] = "---"
	}
	out = append(out, markdownRichTableRow(separators))
	for _, row := range rows {
		out = append(out, markdownRichTableRow(row))
	}
	return out, true
}

func richImageMoreRow(line string, width int) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "...and ") || !strings.HasSuffix(line, " more") {
		return nil, false
	}
	row := make([]string, width)
	row[len(row)-1] = line
	return row, true
}

func richImageSectionKind(section string) string {
	section = strings.TrimSpace(section)
	if start := strings.LastIndex(section, " ("); start >= 0 && strings.HasSuffix(section, ")") {
		section = strings.TrimSpace(section[:start])
	}
	switch {
	case strings.Contains(section, "课表"):
		return "schedule"
	case section == "待办":
		return "todo"
	case section == "作业" || section == "近期作业":
		return "homework"
	case section == "考试":
		return "exam"
	default:
		return ""
	}
}

func overviewScheduleRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 4)
	if !ok {
		return nil, false
	}
	campus, room := scheduleCampusAndRoom(cells[1])
	return []string{campus, room, cells[2], strings.Join(cells[3:], " ")}, true
}

func todoRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 3)
	if !ok {
		return nil, false
	}
	cells[1] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[1]), "截止 "))
	return cells, true
}

func homeworkRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 4)
	if !ok {
		return nil, false
	}
	cells[1] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[1]), "截止 "))
	return cells, true
}

func examRichTableCells(line string) ([]string, bool) {
	return fixedNumberedRichTableCells(line, 7)
}

func fixedNumberedRichTableCells(line string, width int) ([]string, bool) {
	cells, ok := numberedRichTableCells(line)
	if !ok || len(cells) > width {
		return nil, false
	}
	row := make([]string, width)
	copy(row, cells)
	return row, true
}

func numberedRichTableCells(line string) ([]string, bool) {
	cells := richTextTableCells(line)
	if len(cells) < 2 {
		return nil, false
	}
	index := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cells[0]), "."))
	if index == "" {
		return nil, false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	cells[0] = index
	return cells, true
}

func richTextSectionLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	atSectionStart := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			trimmed = strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(trimmed))
		} else {
			out = append(out, line)
		}
		atSectionStart = false
	}
	return out
}

func busRichText(title, text string, query busRouteQuery) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := []string{"# " + strings.TrimSpace(title), ""}
	atHeader := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			atHeader = true
			continue
		}
		cells := richTextTableCells(line)
		if len(cells) == 0 {
			continue
		}
		if atHeader && query.From != "" && query.To != "" {
			for i, cell := range cells {
				if cell == query.From || cell == query.To {
					cells[i] = "**" + cell + "**"
				}
			}
		}
		out = append(out, markdownRichTableRow(cells))
		if atHeader {
			separators := make([]string, len(cells))
			for i := range separators {
				separators[i] = "---"
			}
			out = append(out, "| "+strings.Join(separators, " | ")+" |")
			atHeader = false
		}
	}
	return strings.Join(out, "\n")
}

func richTextTableCells(line string) []string {
	if !strings.Contains(line, "\t") {
		return strings.Fields(line)
	}
	cells := strings.Split(line, "\t")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func busImageRenderText(text string) string {
	return textutil.PlainMonospace(text)
}

func imageRenderText(text string) string {
	text = textutil.PlainMonospace(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\t':
			out.WriteString("  ")
		case '\n':
			out.WriteRune(r)
		case '\r':
			continue
		default:
			if r < ' ' {
				out.WriteRune(' ')
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func imageTitle(text, fallback string) string {
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(text), "\n")[0])
	first = strings.TrimSuffix(first, "：")
	first = strings.TrimSuffix(first, ":")
	if first == "" {
		return fallback
	}
	return first
}

func successfulImageText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	rejectPrefixes := []string{
		"需要先登录",
		"登录未配置",
		"Life @ USTC API unavailable",
		"课表查不到：",
		"待办查不到：",
		"作业查不到：",
		"考试查不到：",
		"下一节课查不到：",
		"教学班查不到：",
		"今日安排查不到：",
		"概览查不到：",
		"近期截止查不到：",
		"校车查不到：",
		"今天后面没查到校车。",
		"没有作业。",
		"没有未完成作业。",
		"该教学班没有作业。",
		"没有订阅课程考试。",
		"该教学班没有考试。",
		"接下来一周没查到课。",
		"需要提供教学班 JW ID。",
	}
	for _, prefix := range rejectPrefixes {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	return true
}

func todoImageArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "list", "all", "pending", "completed":
		return true
	default:
		_, ok := normalizeTodoPriority(args[0])
		return ok
	}
}

func homeworkImageArgs(args []string) bool {
	return !firstArgIn(args, "help", "done", "undo")
}
