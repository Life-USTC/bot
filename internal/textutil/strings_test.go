package textutil

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "fallback", "later"); got != "fallback" {
		t.Fatalf("FirstNonEmpty = %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty empty = %q", got)
	}
}

func TestNonEmpty(t *testing.T) {
	got := NonEmpty("", "one", "", "two")
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
	if got := PlainDigits("作业 𝟷𝟸. Room 3"); got != "作业 12. Room 3" {
		t.Fatalf("PlainDigits = %q", got)
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

func TestPadRightDisplayWide(t *testing.T) {
	if got := PadRightDisplayWide("东区", 3); got != "东区　" {
		t.Fatalf("PadRightDisplayWide = %q", got)
	}
}
