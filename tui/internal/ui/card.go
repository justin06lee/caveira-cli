package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A card is a list in the input's place, drawn the way /model's picker
// is: /mcp's servers, /skills' skills, registry results, servers to
// import. ↑↓ choose; what the other keys do is the card's.

type cardRow struct {
	id     string
	name   string
	detail string
	// status sits at the end of the row, in statusStyle's colour: ok,
	// err, or faint.
	status      string
	statusStyle string
	// picked marks a row chosen for something done to several at once.
	picked   bool
	pickable bool
}

type card struct {
	title string
	rows  []cardRow
	sel   int
	// empty is what the card says when it has no rows; foot is a line
	// under the rows.
	empty string
	foot  string
	// keys are the footer's hints while the card is open.
	keys [][2]string
	// act handles a key other than ↑↓ and esc for the selected row (nil
	// when there are none) and says whether it did.
	act func(k string, row *cardRow) (tea.Cmd, bool)
	// armed is the id of a row waiting on a second x to be removed.
	armed string
	// refresh, when set, reads the rows again before each frame, for a
	// list whose rows change on their own (servers starting).
	refresh func()
}

func (c *card) selected() *cardRow {
	if len(c.rows) == 0 {
		return nil
	}
	c.sel = min(max(c.sel, 0), len(c.rows)-1)
	return &c.rows[c.sel]
}

func (m *Model) openCard(c *card) {
	m.card = c
	m.layout()
}

func (m *Model) closeCard() {
	m.card = nil
	m.layout()
}

func (m *Model) cardKey(k string) (tea.Model, tea.Cmd) {
	c := m.card
	n := len(c.rows)
	switch k {
	case "up", "k", "shift+tab":
		if n > 0 {
			c.sel = (c.sel + n - 1) % n
		}
		c.armed = ""
		return m, nil
	case "down", "j", "tab":
		if n > 0 {
			c.sel = (c.sel + 1) % n
		}
		c.armed = ""
		return m, nil
	case "esc", "ctrl+c", "q":
		m.closeCard()
		return m, nil
	}
	if c.act != nil {
		if cmd, ok := c.act(k, c.selected()); ok {
			m.layout()
			return m, cmd
		}
	}
	return m, nil
}

// renderCard draws the card.
func (m *Model) renderCard() string {
	c := m.card
	if c.refresh != nil {
		c.refresh()
	}
	width := m.inner()
	room := width - 4
	var lines []string
	if len(c.rows) == 0 {
		lines = append(lines, th.Faint.Render(lipgloss.Wrap("  "+c.empty, room, "")))
	}
	nameW := 0
	for _, r := range c.rows {
		nameW = max(nameW, lipgloss.Width(r.name))
	}
	nameW = min(nameW, max(room/3, 12))
	// Short terminals see a window of the list that follows the selection.
	rows := min(len(c.rows), max(3, m.height-18), 10)
	first := min(max(c.sel-rows/2, 0), len(c.rows)-rows)
	for i := first; i < first+rows && i < len(c.rows); i++ {
		r := c.rows[i]
		mark, name := "  ", th.Muted
		if i == c.sel {
			mark, name = th.Accent.Render("›")+" ", th.Title
		}
		if r.pickable {
			box := th.Faint.Render("○ ")
			if r.picked {
				box = th.OK.Render("● ")
			}
			mark += box
		}
		var status string
		switch r.statusStyle {
		case "ok":
			status = th.OK.Render(r.status)
		case "err":
			status = th.Err.Render(r.status)
		default:
			status = th.Faint.Render(r.status)
		}
		if c.armed != "" && c.armed == r.id {
			status = th.Warn.Render("x again to remove")
		}
		label := oneLine(r.name, nameW)
		left := mark + name.Render(fmt.Sprintf("%-*s", nameW, label)) + "   "
		detailRoom := room - lipgloss.Width(left) - lipgloss.Width(status) - 3
		line := left + th.Faint.Render(oneLine(r.detail, max(detailRoom, 4)))
		if status != "" {
			gap := room - lipgloss.Width(line) - lipgloss.Width(status)
			line += strings.Repeat(" ", max(gap, 2)) + status
		}
		lines = append(lines, oneLineANSI(line, room))
	}
	if c.foot != "" {
		lines = append(lines, "", oneLineANSI(c.foot, room))
	}
	return titledBox(c.title, strings.Join(lines, "\n"), width, mix(th.line, th.muted, 0.35))
}
