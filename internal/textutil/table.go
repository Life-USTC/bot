package textutil

import "strings"

type DisplayTableRow struct {
	Cells  []string
	Suffix string
}

type DisplayTableOptions struct {
	EmptyWidthText string
	EmptyCell      func(width int) string
}

func FormatDisplayTable(header []string, rows []DisplayTableRow, options DisplayTableOptions) []string {
	if len(header) == 0 {
		return nil
	}
	width := displayTableCellWidth(header, rows, options.EmptyWidthText)
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, FormatDisplayTableCells(header, width))
	for _, row := range rows {
		cells := make([]string, 0, len(header))
		for i := range header {
			cell := ""
			if i < len(row.Cells) {
				cell = row.Cells[i]
			}
			if cell == "" && options.EmptyCell != nil {
				cell = options.EmptyCell(width)
			}
			cells = append(cells, cell)
		}
		line := FormatDisplayTableCells(cells, width)
		if row.Suffix != "" {
			line += "\t" + row.Suffix
		}
		lines = append(lines, line)
	}
	return lines
}

func FormatDisplayTableCells(cells []string, width int) string {
	out := make([]string, 0, len(cells))
	for _, cell := range cells {
		out = append(out, PadRightDisplay(cell, width))
	}
	return strings.Join(out, "\t")
}

func displayTableCellWidth(header []string, rows []DisplayTableRow, emptyWidthText string) int {
	width := DisplayWidth(emptyWidthText)
	for _, cell := range header {
		width = max(width, DisplayWidth(cell))
	}
	for _, row := range rows {
		for _, cell := range row.Cells {
			if cell != "" {
				width = max(width, DisplayWidth(cell))
			}
		}
	}
	return width
}
