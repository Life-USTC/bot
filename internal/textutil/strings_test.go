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
