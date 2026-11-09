package sanitize

import "testing"

func TestText(t *testing.T) {
	cases := map[string]string{
		"plain text":                 "plain text",
		"a\u202Eevil\u202Cb":         "aevilb",             // right-to-left override
		"zero\u200Bwidth\uFEFF":      "zerowidth",          // zero-width space, BOM
		"bell\x07 and\x1b[31m color": "bell and[31m color", // C0 controls
		"line\r\nend\rold":           "line\nend\nold",
		"tab\tand\nnewline stay":     "tab\tand\nnewline stay",
		"bad \xff byte":              "bad � byte",
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLine(t *testing.T) {
	if got := Line(" Alice\n\u2066Smith\t "); got != "Alice Smith" {
		t.Fatalf("Line = %q", got)
	}
}
