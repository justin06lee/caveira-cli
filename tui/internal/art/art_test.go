package art

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMascotSizes(t *testing.T) {
	for scale := 1; scale <= 3; scale++ {
		for _, style := range []MascotStyle{MascotColor, MascotMonoDark, MascotMonoLight} {
			lines := Mascot(scale, MascotOptions{Style: style})
			if len(lines) != MascotHeight(scale) {
				t.Fatalf("scale %d style %d: %d rows, want %d", scale, style, len(lines), MascotHeight(scale))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != MascotWidth(scale) {
					t.Fatalf("scale %d style %d row %d: %d wide, want %d", scale, style, i, w, MascotWidth(scale))
				}
			}
		}
	}
}

// The skull is drawn in background colour and nothing else: no character
// whose shape comes from the font, so it tiles the same on every terminal
// and at every line spacing.
func TestMascotIsOnlyBackgroundColour(t *testing.T) {
	for _, style := range []MascotStyle{MascotColor, MascotMonoDark, MascotMonoLight} {
		s := ansi.Strip(strings.Join(Mascot(2, MascotOptions{Style: style}), ""))
		if strings.Trim(s, " ") != "" {
			t.Fatalf("style %d draws more than spaces: %q", style, s)
		}
	}
	// A foreground colour would be pointless on a space, and a sign that a
	// glyph crept back in.
	if s := strings.Join(Mascot(1, MascotOptions{Style: MascotColor}), ""); strings.Contains(s, "\x1b[38") {
		t.Fatal("colour skull sets a foreground")
	}
}

var colourSGR = regexp.MustCompile(`\x1b\[(3[0-9]|4[0-9]|9[0-9]|10[0-9])[;m]`)

func TestMonoMascotWritesNoColour(t *testing.T) {
	for _, style := range []MascotStyle{MascotMonoDark, MascotMonoLight} {
		s := strings.Join(Mascot(2, MascotOptions{Style: style}), "\n")
		if colourSGR.MatchString(s) {
			t.Fatalf("style %d wrote a colour: %q", style, s)
		}
		if !strings.Contains(s, "\x1b[7m") {
			t.Fatalf("style %d draws nothing in reverse video", style)
		}
	}
	// The two mono styles are negatives of each other inside the outline.
	dark := strings.Join(Mascot(1, MascotOptions{Style: MascotMonoDark}), "")
	light := strings.Join(Mascot(1, MascotOptions{Style: MascotMonoLight}), "")
	if dark == light {
		t.Fatal("mono styles drew the same picture")
	}
}

// lit counts the cells drawn in reverse video.
func lit(s string) int {
	n, on := 0, false
	for len(s) > 0 {
		switch {
		case strings.HasPrefix(s, "\x1b[7m"):
			on, s = true, s[4:]
		case strings.HasPrefix(s, "\x1b[0m"):
			on, s = false, s[4:]
		default:
			if on && s[0] == ' ' {
				n++
			}
			s = s[1:]
		}
	}
	return n
}

func TestMascotDissolve(t *testing.T) {
	count := func(hidden float64) int {
		return lit(strings.Join(Mascot(2, MascotOptions{Style: MascotMonoDark, Hidden: hidden}), ""))
	}
	full, half, none := count(0), count(0.5), count(1)
	if none != 0 || !(half > 0 && half < full) {
		t.Fatalf("dissolve not progressive: full %d half %d none %d", full, half, none)
	}
}
