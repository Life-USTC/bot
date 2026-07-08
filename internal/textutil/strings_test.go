package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "fallback", "later"); got != "fallback" {
		t.Fatalf("FirstNonEmpty = %q", got)
	}
	if got := FirstNonEmpty("   ", " fallback "); got != "fallback" {
		t.Fatalf("FirstNonEmpty trimmed = %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty empty = %q", got)
	}
}

func TestNonEmpty(t *testing.T) {
	got := NonEmpty("", "one", "   ", " two ")
	want := []string{"one", "two"}
	if len(got) != len(want) {
		t.Fatalf("NonEmpty = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NonEmpty = %#v, want %#v", got, want)
		}
	}
}

func TestJoinNonEmpty(t *testing.T) {
	if got := JoinNonEmpty(" · ", "", " one ", "   ", "two"); got != "one · two" {
		t.Fatalf("JoinNonEmpty = %q", got)
	}
}

func TestHasText(t *testing.T) {
	if !HasText(" value ") {
		t.Fatal("HasText returned false for trimmed text")
	}
	if HasText(" \t\n ") {
		t.Fatal("HasText returned true for whitespace")
	}
}

func TestTrimEqualFold(t *testing.T) {
	if !TrimEqualFold(" GROUP ", "group") {
		t.Fatal("TrimEqualFold did not trim and fold case")
	}
	if TrimEqualFold("private", "group") {
		t.Fatal("TrimEqualFold matched different values")
	}
}

func TestLowerTrim(t *testing.T) {
	if got := LowerTrim(" KB "); got != "kb" {
		t.Fatalf("LowerTrim = %q", got)
	}
}

func TestTrimTrailingSlash(t *testing.T) {
	if got := TrimTrailingSlash(" https://life.example/// "); got != "https://life.example" {
		t.Fatalf("TrimTrailingSlash = %q", got)
	}
	if got := TrimTrailingSlash("   "); got != "" {
		t.Fatalf("TrimTrailingSlash blank = %q", got)
	}
}

func TestTrimBytesRunes(t *testing.T) {
	got := TrimBytesRunes([]byte(" \n"+strings.Repeat("错", 201)+" "), 200)
	if !utf8.ValidString(got) {
		t.Fatalf("TrimBytesRunes returned invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 200 {
		t.Fatalf("rune count = %d", utf8.RuneCountInString(got))
	}
	if got != strings.Repeat("错", 200) {
		t.Fatalf("TrimBytesRunes = %q", got)
	}
}

func TestIndexASCIIToken(t *testing.T) {
	tests := []struct {
		text  string
		token string
		want  int
	}{
		{text: "bus from east", token: "bus", want: 0},
		{text: "take northeast then north", token: "north", want: 20},
		{text: "northeast", token: "north", want: -1},
		{text: "bus2", token: "bus", want: -1},
		{text: "to-bus", token: "bus", want: 3},
		{text: "bus", token: "", want: -1},
	}
	for _, tt := range tests {
		if got := IndexASCIIToken(tt.text, tt.token); got != tt.want {
			t.Fatalf("IndexASCIIToken(%q, %q) = %d, want %d", tt.text, tt.token, got, tt.want)
		}
	}
}

func TestMonospaceDigits(t *testing.T) {
	if got := MonospaceDigits("Room 3A204"); got != "Room 𝟹A𝟸𝟶𝟺" {
		t.Fatalf("MonospaceDigits = %q", got)
	}
}

func TestMonospaceASCII(t *testing.T) {
	if got := MonospaceASCII("MATH1001.01"); got != "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷" {
		t.Fatalf("MonospaceASCII = %q", got)
	}
}

func TestPlainDigits(t *testing.T) {
	if got := PlainDigits("作业 𝟷𝟸. Room ３"); got != "作业 12. Room 3" {
		t.Fatalf("PlainDigits = %q", got)
	}
}

func TestPlainMonospace(t *testing.T) {
	if got := PlainMonospace("𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷 Room ３"); got != "MATH1001 Room 3" {
		t.Fatalf("PlainMonospace = %q", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	if got := DisplayWidth("A中𝟷"); got != 4 {
		t.Fatalf("DisplayWidth = %d", got)
	}
}

func TestPadRightDisplay(t *testing.T) {
	if got := PadRightDisplay("东区", 6); got != "东区  " {
		t.Fatalf("PadRightDisplay wide text = %q", got)
	}
	if got := PadRightDisplay("abcdef", 3); got != "abcdef" {
		t.Fatalf("PadRightDisplay long text = %q", got)
	}
	if got := PadRightDisplay("", 3); got != "" {
		t.Fatalf("PadRightDisplay empty = %q", got)
	}
}

func TestPadRightDisplayWith(t *testing.T) {
	if got := PadRightDisplayWith("东区", 6, "\u3000"); got != "东区　" {
		t.Fatalf("PadRightDisplayWith wide pad = %q", got)
	}
	if got := PadRightDisplayWith("东区", 6, ""); got != "东区" {
		t.Fatalf("PadRightDisplayWith empty pad = %q", got)
	}
}

func TestPadRightDisplayWide(t *testing.T) {
	if got := PadRightDisplayWide("东区", 3); got != "东区　" {
		t.Fatalf("PadRightDisplayWide = %q", got)
	}
}
