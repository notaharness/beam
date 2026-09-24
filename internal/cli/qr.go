package cli

import (
	"io"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
)

// quiet is the QR code's quiet zone in modules (docs/07).
const quiet = 3

// braille holds the bit of each module in a braille cell, [row][column]: the
// cell's dots 1–8.
var braille = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// drawQR writes text as a QR code at level L, each character cell a braille
// pattern of 2 × 4 modules, a light module a raised dot, white on black.
func drawQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.L, qr.Auto)
	if err != nil {
		return err
	}
	var b strings.Builder
	n := code.Bounds().Dx() + 2*quiet
	for y := 0; y < n; y += 4 {
		b.WriteString("\x1b[97;40m")
		for x := 0; x < n; x += 2 {
			b.WriteRune(cell(code, x, y))
		}
		b.WriteString("\x1b[0m\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// cell is the braille pattern for the modules from (x, y), counted from the
// quiet zone's corner; a module beyond the code is light.
func cell(code barcode.Barcode, x, y int) rune {
	r := rune(0x2800)
	for dy, row := range braille {
		for dx, bit := range row {
			if !dark(code, x+dx-quiet, y+dy-quiet) {
				r |= bit
			}
		}
	}
	return r
}

func dark(code barcode.Barcode, x, y int) bool {
	b := code.Bounds()
	if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
		return false
	}
	r, _, _, _ := code.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return r == 0
}
