package textutil

import (
	"strings"
	"testing"
)

func TestFormatDisplayTablePadsCellsAndSuffix(t *testing.T) {
	lines := FormatDisplayTable(
		[]string{"东区", "高新区"},
		[]DisplayTableRow{
			{Cells: []string{MonospaceDigits("09:00"), MonospaceDigits("09:40")}, Suffix: "✨"},
			{Cells: []string{MonospaceDigits("10:00"), ""}},
		},
		DisplayTableOptions{
			EmptyWidthText: "———",
			EmptyCell: func(width int) string {
				return strings.Repeat("\u3000", width/2) + strings.Repeat(" ", width%2)
			},
		},
	)
	got := strings.Join(lines, "\n")
	want := strings.Join([]string{
		"东区  \t高新区",
		"𝟶𝟿:𝟶𝟶 \t𝟶𝟿:𝟺𝟶 \t✨",
		"𝟷𝟶:𝟶𝟶 \t　　　",
	}, "\n")
	if got != want {
		t.Fatalf("table = %q, want %q", got, want)
	}
}

func TestFormatDisplayTableCells(t *testing.T) {
	if got := FormatDisplayTableCells([]string{"东区", "西区"}, 5); got != "东区 \t西区 " {
		t.Fatalf("cells = %q", got)
	}
}
