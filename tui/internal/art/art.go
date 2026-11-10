// Package art renders caveira's pixel skull mascot as terminal cells. A
// pixel is drawn in the cell's background colour, never in a block
// character: a terminal paints a background out to the cell's edges whatever
// the font or line spacing, where ▀ and █ come from the font and stop short
// of the row on many setups (xterm.js with Meslo, for one), which tears the
// skull into seams and uneven rows. A cell is about twice as tall as it is
// wide, so a pixel is two cells wide and one tall to come out square.
// In colour a pixel is a pair of background-coloured spaces; in mono it is
// a pair of spaces in reverse video, which takes the terminal's own
// foreground.
package art

import (
	"fmt"
	"math"
	"strings"
)

// PixelW is how many cells wide a pixel is drawn.
const PixelW = 2

// Pixel is one straight-alpha colour sample.
type Pixel struct {
	R, G, B float64 // 0..1
	A       float64 // 0..1
}

// Bitmap is a grid of pixels ready to be drawn.
type Bitmap struct {
	W, H int
	Pix  []Pixel
}

func (b *Bitmap) At(x, y int) Pixel {
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		return Pixel{}
	}
	return b.Pix[y*b.W+x]
}

// Options shape how a bitmap becomes cells.
type Options struct {
	// Brightness scales every colour; 1 (or 0) is as drawn.
	Brightness float64
	// Mono ignores colour: a pixel is either on, drawn in reverse video so
	// it takes the terminal's foreground, or off. Nothing but spaces and
	// SGR 7 is written.
	Mono bool
}

// Render draws a bitmap as one row of cells per pixel row, each pixel
// PixelW cells wide. Transparent pixels are plain spaces with no colour, so
// they show whatever is behind them. Neighbours of one colour share an
// escape, and every row ends with the attributes reset.
func Render(b *Bitmap, o Options) []string {
	if o.Brightness == 0 {
		o.Brightness = 1
	}
	out := make([]string, b.H)
	var sb strings.Builder
	for y := 0; y < b.H; y++ {
		sb.Reset()
		cur := "" // the SGR in force; "" is none
		for x := 0; x < b.W; x++ {
			p := b.At(x, y)
			want := ""
			if p.A > 0.35 {
				want = "\x1b[7m"
				if !o.Mono {
					want = bg(p, o.Brightness)
				}
			}
			if want != cur {
				if cur != "" {
					sb.WriteString("\x1b[0m")
				}
				sb.WriteString(want)
				cur = want
			}
			sb.WriteString(strings.Repeat(" ", PixelW))
		}
		if cur != "" {
			sb.WriteString("\x1b[0m")
		}
		out[y] = sb.String()
	}
	return out
}

func clamp8(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 255
	}
	return int(math.Round(v * 255))
}

func bg(p Pixel, s float64) string {
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", clamp8(p.R*s), clamp8(p.G*s), clamp8(p.B*s))
}

// hash is a cheap, stable 0..1 value per cell.
func hash(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h&0xffff) / 0xffff
}
