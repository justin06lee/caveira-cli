package ui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/tools"
	"github.com/justin06lee/caveira/tui/internal/art"
)

// These tests draw the screen at every size a terminal is likely to be, with
// values as long as they come, and hold it to what a terminal needs: the
// right number of rows, no row wider than the screen (a terminal wraps it,
// and everything under it moves), and edges that are the same cell on every
// row. Widths are display widths, as a terminal counts them, not bytes or
// runes.

var sizeMatrix = [][2]int{{40, 12}, {60, 20}, {80, 24}, {100, 30}, {120, 40}, {200, 60}}

// The values a real machine has: a 60-plus character account, a deep path,
// a long branch, a long model name.
const (
	longAccount = "someone.with.a.very.long.address.1234567890@example-company-name.co.uk"
	longDir     = "/Users/someone/Workspace/github.com/organisation/some-long-repository-name/packages/deep/nested/dir"
	longBranch  = "feature/a-very-long-branch-name-for-the-fix"
	longModel   = "some-local-model-with-a-long-name:latest-quantized"
)

// hexBg is how a colour's background is written in an SGR, "r;g;b".
func hexBg(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("%d;%d;%d", r>>8, g>>8, b>>8)
}

func surfaceBg() string { return hexBg(th.surface) }

// cellBgs is the background of every cell of a styled string, "" where a
// cell has none. A line break resets nothing, as in a terminal.
func cellBgs(s string) []string {
	var out []string
	bg := ""
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				ps := strings.Split(s[i+2:j], ";")
				for k := 0; k < len(ps); k++ {
					switch ps[k] {
					case "", "0", "49":
						bg = ""
					case "48":
						if k+1 < len(ps) && ps[k+1] == "2" && k+4 < len(ps) {
							bg = strings.Join(ps[k+2:k+5], ";")
							k += 4
						} else if k+1 < len(ps) && ps[k+1] == "5" && k+2 < len(ps) {
							bg = "256:" + ps[k+2]
							k += 2
						}
					case "38":
						if k+1 < len(ps) && ps[k+1] == "2" {
							k += 4
						} else if k+1 < len(ps) && ps[k+1] == "5" {
							k += 2
						}
					}
				}
			}
			i = j + 1
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == '\n' {
			continue
		}
		for range max(lipgloss.Width(string(r)), 0) {
			out = append(out, bg)
		}
	}
	return out
}

// countBg is how many cells of s sit on background want; "" counts the
// cells that have any background.
func countBg(s, want string) int {
	n := 0
	for _, bg := range cellBgs(s) {
		if bg != "" && (want == "" || bg == want) {
			n++
		}
	}
	return n
}

// frameFits holds a frame to the screen: h rows, none wider than w.
func frameFits(t *testing.T, label string, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d rows, want %d", label, len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			t.Errorf("%s: row %d is %d wide on a %d-wide screen: %q", label, i, lw, w, plain(l))
			return
		}
	}
}

// longen gives a model the long values, wherever they are kept.
func longen(m *Model) {
	m.account, m.workDir, m.branch = longAccount, longDir, longBranch
	m.agent.Model = longModel
	if len(m.items) > 0 && m.items[0].kind == itemHeader {
		h := m.items[0].header
		h.account, h.dir, h.branch, h.model = longAccount, longDir, longBranch, longModel
		m.items[0].invalidate()
		m.dirty = true
	}
	m.layout()
}

func TestEverySizeFitsWithLongValues(t *testing.T) {
	for _, sz := range sizeMatrix {
		w, h := sz[0], sz[1]
		for _, long := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d long=%v", w, h, long)
			m := testModel(t, w, h)
			m.account = "demo-account-123@example.test"
			if long {
				longen(m)
			}
			home, _ := m.renderHome(0)
			frameFits(t, name+" home", home, w, h)
			mid, _ := m.renderHome(0.5)
			frameFits(t, name+" slide", mid, w, h)
			end, _ := m.renderHome(1)
			frameFits(t, name+" landed", end, w, h)
			m.phase = phaseSplash
			for _, frame := range []int{6, 20, bootFrames} {
				m.splash.frame = frame
				intro, _ := m.renderHome(0)
				frameFits(t, fmt.Sprintf("%s intro %d", name, frame), intro, w, h)
			}

			s := session(t, w, h)
			s.account = "demo-account-123@example.test"
			if long {
				longen(s)
			}
			frameFits(t, name+" session", s.View().Content, w, h)

			s.running = true
			s.streamed = 4000
			s.notice = "rate limited, retrying in 12s because the model is busy right now"
			s.queued = []string{"a queued message that is long enough to wrap on the narrow terminals in this test", "and another"}
			s.dirty = true
			s.layout()
			frameFits(t, name+" running", s.View().Content, w, h)
			s.running, s.queued, s.notice = false, nil, ""

			s.input.SetValue("/")
			s.layout()
			frameFits(t, name+" palette", s.View().Content, w, h)
			s.input.SetValue("one\ntwo\nthree\nfour")
			s.layout()
			frameFits(t, name+" tall input", s.View().Content, w, h)
			s.input.SetValue("")
			s.layout()

			s.approval = &agent.ApprovalEvent{Name: "edit_file", Preview: longDir + "/file_that_is_long.go", Kind: tools.KindWrite, Reply: make(chan agent.Decision, 1)}
			s.layout()
			frameFits(t, name+" approval", s.View().Content, w, h)
		}
	}
}

// The skull is there when there is height for it and gone when there is
// not, whole either way: every row the same width, and nothing but cells of
// background colour, so no font or line spacing can seam or shorten it.
func TestSkullComesAndGoesWithTheSize(t *testing.T) {
	want := map[[2]int]int{{40, 12}: 0, {60, 20}: 0, {80, 24}: 1, {100, 30}: 1, {120, 40}: 2, {200, 60}: 2}
	for _, sz := range sizeMatrix {
		w, h := sz[0], sz[1]
		m := testModel(t, w, h)
		frame, _ := m.renderHome(0)
		scale := m.homeLayout().scale
		if scale != want[sz] {
			t.Errorf("%dx%d: skull scale %d, want %d", w, h, scale, want[sz])
		}
		if strings.ContainsAny(plain(frame), "▀▄█▗▖▝▘") {
			t.Errorf("%dx%d: block characters in the home screen", w, h)
		}
		// The skull's cells are the ones with a background that is not the
		// prompt box's panel.
		rows := 0
		for _, l := range strings.Split(frame, "\n") {
			if cells := countBg(l, "") - countBg(l, surfaceBg()); cells > 0 {
				rows++
				if cells%art.PixelW != 0 {
					t.Errorf("%dx%d: a skull row has %d cells of colour, not whole pixels", w, h, cells)
				}
			}
		}
		wantRows := 0
		if scale > 0 {
			wantRows = art.MascotHeight(scale)
		}
		if rows != wantRows {
			t.Errorf("%dx%d: %d rows of skull, want %d", w, h, rows, wantRows)
		}
	}
}

// The prompt box is a panel of one colour: its padding rows and its text
// row are the same width and every cell of them is on the panel, which is to
// say a text area that forgot the background would show as a hole.
func TestPromptBoxIsOnePanelAtEverySize(t *testing.T) {
	for _, sz := range sizeMatrix {
		w, h := sz[0], sz[1]
		check := func(label, frame string, boxW, x int) {
			t.Helper()
			at := boxRow(frame)
			if at < 1 {
				t.Errorf("%s: no prompt box", label)
				return
			}
			rows := strings.Split(frame, "\n")
			for _, r := range []int{at - 1, at, at + 1} {
				cells := cellBgs(rows[r])
				got := 0
				for i, bg := range cells {
					if bg == surfaceBg() {
						got++
					} else if i >= x && i < x+boxW {
						t.Errorf("%s: row %d cell %d is off the panel: %q", label, r, i, bg)
						return
					}
				}
				if got != boxW {
					t.Errorf("%s: row %d has %d panel cells, want %d", label, r, got, boxW)
				}
			}
			if !strings.Contains(strings.Split(plain(frame), "\n")[at], strings.Repeat(" ", x+2)+"› ") {
				t.Errorf("%s: mark is not two cells in from the panel's edge", label)
			}
		}
		m := testModel(t, w, h)
		home, _ := m.renderHome(0)
		f := m.homeLayout()
		check(fmt.Sprintf("%dx%d home", w, h), home, f.boxW, sideMargin+f.boxX)
		s := session(t, w, h)
		check(fmt.Sprintf("%dx%d session", w, h), s.View().Content, s.inner(), sideMargin)
		s.input.SetValue("a line\nand another that is long enough to wrap around the inside of a narrow box, going on and on")
		s.layout()
		check(fmt.Sprintf("%dx%d session, two lines", w, h), s.View().Content, s.inner(), sideMargin)
	}
}

// The line under the home box is cut only when it does not fit: at the
// narrowest size the model and effort still read whole, and a long model name
// gives way with an ellipsis rather than the line wrapping.
func TestHomeInfoLineIsCutOnlyWhenItMustBe(t *testing.T) {
	m := testModel(t, 40, 12)
	home, _ := m.renderHome(0)
	if !strings.Contains(plain(home), "qwen-3.5 · high effort") {
		t.Fatalf("model and effort cut at 40 wide:\n%s", plain(home))
	}
	longen(m)
	home, _ = m.renderHome(0)
	if p := plain(home); !strings.Contains(p, "some-local-model-with-a-long-name") || !strings.Contains(p, "…") {
		t.Fatalf("long model not cut with an ellipsis:\n%s", p)
	}
}

// A terminal that cannot shade gets the outline, in the same rows.
func TestPromptBoxFallsBackToAnOutline(t *testing.T) {
	m := testModel(t, 80, 24)
	m.profile = colorprofile.ANSI
	m.applyTheme()
	box := m.renderInputBox(70)
	if !strings.Contains(box, "╭") || strings.Contains(box, "48;") {
		t.Fatalf("no outline without shading:\n%s", box)
	}
	if h := lipgloss.Height(box); h != m.input.Height()+2 {
		t.Fatalf("outline is %d rows, the panel is %d", h, m.input.Height()+2)
	}
}

// The startup card is lines of text, with no skull beside them; the skull
// is the home screen's. No box to dash, no line wrapped, and the labels and
// the values start in the same columns on every row.
func TestStartupCardAtEverySize(t *testing.T) {
	for _, sz := range sizeMatrix {
		w, h := sz[0], sz[1]
		for _, long := range []bool{false, true} {
			s := session(t, w, h)
			s.account = "demo-account-123@example.test"
			if long {
				longen(s)
			}
			s.items[0].header.account = s.account
			s.items[0].invalidate()
			card := s.rend.render(s.items[0])
			name := fmt.Sprintf("%dx%d long=%v", w, h, long)
			rows := strings.Split(card, "\n")
			text := strings.Split(plain(card), "\n")
			indent := func(l string) int { return lipgloss.Width(l) - lipgloss.Width(strings.TrimLeft(l, " ")) }
			found := map[string]int{}
			for i, l := range rows {
				if lw := lipgloss.Width(l); lw > s.inner() {
					t.Errorf("%s: card row %d is %d wide, the screen holds %d: %q", name, i, lw, s.inner(), text[i])
				}
				trimmed := strings.TrimSpace(text[i])
				for _, label := range []string{"model", "cwd", "account"} {
					if trimmed == label {
						t.Errorf("%s: label %q is alone on its row", name, label)
					}
					if strings.HasPrefix(trimmed, label+"  ") {
						found[label] = indent(text[i])
					}
				}
			}
			for _, label := range []string{"model", "cwd", "account"} {
				if _, ok := found[label]; !ok {
					t.Errorf("%s: no %s row in the card:\n%s", name, label, strings.Join(text, "\n"))
				}
			}
			if found["model"] != found["cwd"] || found["model"] != found["account"] {
				t.Errorf("%s: the labels start in different columns: %v", name, found)
			}
			if strings.ContainsAny(strings.Join(text, ""), "╭╮╰╯│─") {
				t.Errorf("%s: the card is boxed in line-drawing characters", name)
			}
			if countBg(card, "") > 0 {
				t.Errorf("%s: the card should be text only, with no skull", name)
			}
			if found["model"] != 0 {
				t.Errorf("%s: the card's text is indented %d", name, found["model"])
			}
		}
	}
}

// Every row of a bubble is as wide as the others, in fill and in cells, at
// every width and for text a terminal counts oddly: a word longer than the
// bubble, wide characters, and tabs.
func TestBubbleEdgesLineUpAtEveryWidth(t *testing.T) {
	_ = testModel(t, 80, 24)
	texts := []string{
		"hi",
		"The invoice total is a cent off on some orders. Find out why and fix it.",
		strings.Repeat("make the retry logic back off exponentially ", 6),
		"https://example.com/" + strings.Repeat("a-very-long-path-segment/", 8),
		"日本語のメッセージ、とても長い文章です。折り返しが正しいか確かめます。",
		"two\nlines\n\nand a gap",
		"a\ttab",
	}
	for _, width := range []int{22, 38, 58, 78, 98, 118, 198} {
		for _, text := range texts {
			text = strings.ReplaceAll(text, "\t", "    ")
			out := bubble(text, width, th.bubble, th.text, th.muted)
			raw := strings.Split(out, "\n")
			first := countBg(raw[0], hexBg(th.bubble))
			for i, l := range raw {
				if lw := lipgloss.Width(l); lw != width {
					t.Fatalf("width %d, %q: row %d is %d wide, want %d (flush right)", width, text, i, lw, width)
				}
				if got := countBg(l, hexBg(th.bubble)); got != first {
					t.Fatalf("width %d, %q: row %d has %d cells of fill, the first has %d", width, text, i, got, first)
				}
			}
			if first > max(width*3/4, 24) && first > width {
				t.Fatalf("width %d, %q: bubble %d cells wide", width, text, first)
			}
		}
	}
}

// Resizing while it runs: every size in turn, from the boot screen, the home
// screen, and a session, and the frame fits each time. This goes through
// the same WindowSizeMsg a terminal's SIGWINCH becomes.
func TestResizingWhileRunning(t *testing.T) {
	steps := [][2]int{{80, 24}, {40, 12}, {200, 60}, {60, 20}, {120, 40}, {100, 30}, {40, 12}, {80, 24}}
	for _, phase := range []string{"hero", "session"} {
		var m *Model
		if phase == "hero" {
			m = testModel(t, 80, 24)
			longen(m)
		} else {
			m = session(t, 80, 24)
			longen(m)
		}
		for _, sz := range steps {
			next, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m = next.(*Model)
			v := m.View()
			frameFits(t, fmt.Sprintf("%s after resize to %dx%d", phase, sz[0], sz[1]), v.Content, sz[0], sz[1])
			if v.Cursor != nil && (v.Cursor.Position.X >= sz[0] || v.Cursor.Position.Y >= sz[1]) {
				t.Errorf("%s %dx%d: cursor at %v is off the screen", phase, sz[0], sz[1], v.Cursor.Position)
			}
		}
	}
}
