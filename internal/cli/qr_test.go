package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/boombuler/barcode/qr"
)

// docs/07 Enrolment: the QR code is the URL at level L in braille cells of
// 2 × 4 modules, light modules raised, white on black, with a three-module
// quiet zone; read back module by module, it is the encoder's code.
func TestDrawQR(t *testing.T) {
	for _, tc := range []struct {
		url        string
		cols, rows int
	}{
		{"https://beam.n10.is/#c=" + strings.Repeat("A", 43) + "&f=b7f39a210c4e55d1&k=" + strings.Repeat("B", 43) +
			"&l=buildbox&o=a&s=" + strings.Repeat("C", 22), 28, 14}, // 179 characters, version 8
		{"https://beam.n10.is/#c=" + strings.Repeat("A", 43) + "&f=b7f39a210c4e55d1&k=" + strings.Repeat("B", 43) +
			"&l=" + strings.Repeat("a", 64) + "&o=r&s=" + strings.Repeat("C", 22), 30, 15}, // a 64-character label, version 9
	} {
		var out bytes.Buffer
		if err := drawQR(&out, tc.url); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != tc.rows {
			t.Fatalf("%d rows, want %d", len(lines), tc.rows)
		}
		code, _ := qr.Encode(tc.url, qr.L, qr.Auto)
		n := code.Bounds().Dx()
		for y, line := range lines {
			cells, ok := strings.CutPrefix(line, "\x1b[97;40m")
			cells, ok2 := strings.CutSuffix(cells, "\x1b[0m")
			if !ok || !ok2 || len([]rune(cells)) != tc.cols {
				t.Fatalf("row %d: %q, want %d cells white on black", y, line, tc.cols)
			}
			for x, r := range []rune(cells) {
				for dy, row := range [4][2]uint{{1, 4}, {2, 5}, {3, 6}, {7, 8}} { // Unicode's braille dot numbers
					for dx, dot := range row {
						mx, my := 2*x+dx-quiet, 4*y+dy-quiet
						inside := mx >= 0 && my >= 0 && mx < n && my < n
						dark := inside && func() bool { r, _, _, _ := code.At(mx, my).RGBA(); return r == 0 }()
						if raised := (r-0x2800)&(1<<(dot-1)) != 0; raised == dark {
							t.Fatalf("module (%d, %d): raised %v, dark %v", mx, my, raised, dark)
						}
					}
				}
			}
		}
	}
}
