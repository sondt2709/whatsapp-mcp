// Package sanitize removes the characters from untrusted text that can hide
// content from a person or change how a terminal or an agent shows it.
package sanitize

import (
	"strings"
	"unicode"
)

// hidden reports whether a rune must be removed: bidi controls, zero-width
// characters, and C0 and C1 control characters except newline and tab.
func hidden(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true // bidi embedding, override and isolate controls
	case r >= 0x200B && r <= 0x200F, r == 0x2060, r == 0xFEFF:
		return true // zero-width characters and marks
	case r == 0x061C:
		return true // Arabic letter mark
	case unicode.IsControl(r):
		return true // C0, DEL and C1
	}
	return false
}

// Text removes hidden characters, makes the text valid UTF-8 and changes
// "\r\n" and "\r" to "\n".
func Text(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if hidden(r) {
			return -1
		}
		return r
	}, s)
}

// Line is Text on one line: it also changes newlines and tabs to spaces.
// Use it for names.
func Line(s string) string {
	return strings.Join(strings.Fields(Text(s)), " ")
}
