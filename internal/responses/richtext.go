package responses

import "strings"

type richDocument struct {
	Title  string
	Blocks []richBlock
}

type richBlock struct {
	Heading string
	Lines   []string
	Table   *busRenderTable
}

func parseRichText(text string) richDocument {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n")
	doc := richDocument{}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		doc.Title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "# "))
		lines = lines[1:]
	}
	for len(lines) > 0 {
		for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
			lines = lines[1:]
		}
		if len(lines) == 0 {
			break
		}
		heading := ""
		if value, ok := richSectionHeading(lines[0]); ok {
			heading = value
			lines = lines[1:]
			for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
				lines = lines[1:]
			}
		}
		if len(lines) == 0 {
			doc.Blocks = append(doc.Blocks, richBlock{Heading: heading})
			break
		}
		if isRichTableLine(lines[0]) {
			block := []string{}
			for len(lines) > 0 && isRichTableLine(lines[0]) {
				block = append(block, lines[0])
				lines = lines[1:]
			}
			if table := parseRichTable(block); table != nil {
				doc.Blocks = append(doc.Blocks, richBlock{Heading: heading, Table: table})
			}
			continue
		}
		block := []string{}
		for len(lines) > 0 && strings.TrimSpace(lines[0]) != "" && !isRichTableLine(lines[0]) {
			if _, ok := richSectionHeading(lines[0]); ok {
				break
			}
			block = append(block, strings.TrimSpace(lines[0]))
			lines = lines[1:]
		}
		if heading != "" || len(block) > 0 {
			doc.Blocks = append(doc.Blocks, richBlock{Heading: heading, Lines: block})
		}
	}
	if doc.Title == "" {
		doc.Title = "Life @ USTC"
	}
	return doc
}

func richSectionHeading(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "## ") {
		return "", false
	}
	heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
	return heading, heading != ""
}

func isRichTableLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|")
}

func parseRichTable(lines []string) *busRenderTable {
	if len(lines) < 2 {
		return nil
	}
	header := richTableCells(lines[0])
	if len(header) == 0 {
		return nil
	}
	headerEmphasis := make([]bool, len(header))
	for i, cell := range header {
		header[i], headerEmphasis[i] = richStrongCell(cell)
	}
	start := 1
	if richTableSeparator(lines[1]) {
		start = 2
	}
	table := &busRenderTable{Header: header, HeaderEmphasis: headerEmphasis}
	for _, line := range lines[start:] {
		cells := richTableCells(line)
		if len(cells) == 0 {
			continue
		}
		row := busRenderRow{}
		if cells[len(cells)-1] == "✨" {
			row.Highlight = true
			cells = cells[:len(cells)-1]
		}
		if len(cells) > len(header) {
			cells = cells[:len(header)]
		}
		row.Cells = cells
		table.Rows = append(table.Rows, row)
	}
	if len(table.Rows) == 0 {
		return nil
	}
	return table
}

func richStrongCell(cell string) (string, bool) {
	cell = strings.TrimSpace(cell)
	if len(cell) >= 4 && strings.HasPrefix(cell, "**") && strings.HasSuffix(cell, "**") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(cell, "**"), "**")), true
	}
	return cell, false
}

func richTableCells(line string) []string {
	line = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|"))
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func richTableSeparator(line string) bool {
	for _, cell := range richTableCells(line) {
		trimmed := strings.Trim(cell, " :-")
		if trimmed != "" || !strings.Contains(cell, "---") {
			return false
		}
	}
	return true
}
