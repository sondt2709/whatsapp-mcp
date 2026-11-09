package main

import (
	"strings"

	"rsc.io/qr"
)

// quiet is the white border of the QR code, in modules.
const quiet = 2

// qrText draws a QR code with half-block characters: two modules for each
// character line. It sets black on white with ANSI codes, so that the code
// scans in a dark terminal too.
func qrText(text string) (string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := -quiet; x < code.Size+quiet; x++ {
			top, bottom := code.Black(x, y), code.Black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
