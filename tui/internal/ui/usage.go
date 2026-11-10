package ui

import (
	"context"
	"fmt"
	"github.com/justin06lee/caveira/core/devserver"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/justin06lee/caveira/core/cloud"
)

// usageInfo is the account's period as the Usage panel draws it: the plan
// on one line, then a bar as wide as the terminal allows. It is never in
// dollars.
type usageInfo struct {
	plan   string  // "Lightweight · 10x the usage of Free", "Admin · unlimited"
	admin  bool    // no allowance, so no bar
	used   float64 // share of the period spent, 0 to 1
	resets string  // "resets Nov 20", "" when the site did not say
	// problem is why there is no plan to show: signed out, or the site
	// could not be asked.
	problem string
}

// accountMsg is what the site said about the account. panel says /usage
// asked, and waits on it to draw.
type accountMsg struct {
	acct    cloud.Account
	err     error
	missing bool // not signed in
	panel   bool
}

// fetchAccount asks the site about the account in the background.
func fetchAccount(panel bool) tea.Cmd {
	return func() tea.Msg {
		a, ok := cloud.Load()
		if !ok {
			return accountMsg{missing: true, panel: panel}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		acct, err := cloud.Me(ctx, a)
		return accountMsg{acct: acct, err: err, panel: panel}
	}
}

// watchUsage asks after the account once a reply is in, so crossing 80%
// or the whole allowance gets a line. Only an account has an allowance.
func (m *Model) watchUsage() tea.Cmd {
	if !m.cfg.OnAccount() {
		return nil
	}
	return fetchAccount(false)
}

// showUsage is /usage: on an account it waits on the site for the plan
// and the bar; signed out, it shows the session's tokens.
func (m *Model) showUsage() tea.Cmd {
	if !m.cfg.OnAccount() {
		m.push(m.costPanel(nil))
		return nil
	}
	return fetchAccount(true)
}

func (m *Model) accountAnswered(msg accountMsg) {
	var u usageInfo
	switch {
	case msg.missing:
		u.problem = "signed out · /login"
	case msg.err != nil:
		u.problem = "couldn't reach caveira.app"
	default:
		u = usageOf(msg.acct, time.Now())
		m.noteUsage(u)
	}
	if msg.panel {
		m.push(m.costPanel(&u))
		m.follow = true
	}
}

func usageOf(acct cloud.Account, now time.Time) usageInfo {
	return usageInfo{
		plan:   acct.PlanLine(),
		admin:  acct.Admin,
		used:   acct.Usage.Used,
		resets: resetsText(acct.Usage.ResetsAt, now),
	}
}

// noteUsage says, in one quiet line, when a reply has taken the account
// past 80% of its period's usage, and again when it is all used. The
// first answer of a session only sets where it stood.
func (m *Model) noteUsage(u usageInfo) {
	prev, known := m.usageSeen, m.usageKnown
	m.usageSeen, m.usageKnown = u.used, true
	if !known || u.admin {
		return
	}
	var text string
	switch {
	case prev < 1 && u.used >= 1:
		text = "this period's usage is all used"
	case prev <= usageHigh && u.used > usageHigh:
		text = percentUsed(u.used) + " of this period's usage used"
	default:
		return
	}
	if u.resets != "" {
		text += " · " + u.resets
	}
	m.push(&item{kind: itemNotice, text: text + " · /usage", warn: true})
}

// usageHigh is past where the bar turns amber.
const usageHigh = 0.8

// resetsText says when usage starts again: the time within a day, the
// date after that. The period turns over in UTC, so the date is UTC's, as
// the site and the app name it.
func resetsText(iso string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	if t.Sub(now) < 24*time.Hour {
		return "resets at " + t.Local().Format("3:04 PM")
	}
	return "resets " + t.UTC().Format("Jan 2")
}

// percentUsed rounds the share to a whole percent, and never says 100%
// before it is.
func percentUsed(used float64) string {
	pct := int(math.Round(100 * math.Max(0, used)))
	if used < 1 && pct >= 100 {
		pct = 99
	}
	if used >= 1 {
		pct = 100
	}
	return fmt.Sprintf("%d%%", pct)
}

// usageTone is the bar's colour: the theme's bone, amber past 80%, red
// once it is all used.
func usageTone(used float64) lipgloss.Style {
	switch {
	case used >= 1:
		return th.Err
	case used > usageHigh:
		return th.Warn
	}
	return th.Bone
}

// usageBar is used drawn in cells: filled for what is spent, shaded for
// what is left.
func usageBar(used float64, cells int) string {
	n := int(math.Round(math.Min(1, math.Max(0, used)) * float64(cells)))
	if used < 1 && n == cells {
		n = cells - 1
	}
	return usageTone(used).Render(strings.Repeat("█", n)) + th.Faint.Render(strings.Repeat("░", cells-n))
}

// Bar widths: as wide as the terminal leaves room for, up to barMax.
const barMin, barMax = 8, 40

// renderUsage is the panel's top: the plan, and under it the bar with how
// much is used, sized to width.
func renderUsage(u *usageInfo, width int) []string {
	const pad = "    "
	if u.problem != "" {
		return []string{pad + th.Muted.Render(u.problem)}
	}
	out := []string{pad + th.Text.Render(u.plan)}
	if u.admin {
		return out
	}
	label := percentUsed(u.used) + " used"
	if u.resets != "" {
		label += " · " + u.resets
	}
	tone := th.Muted
	if u.used > usageHigh {
		tone = usageTone(u.used)
	}
	room := width - len(pad) - 2 - lipgloss.Width(label)
	if room >= barMin {
		return append(out, pad+usageBar(u.used, min(room, barMax))+"  "+tone.Render(label))
	}
	// Too narrow for both on one line: the bar, then the words.
	return append(out, pad+usageBar(u.used, max(width-len(pad), 1)), pad+tone.Render(label))
}

// costPanel is /usage: the account's period when there is one (u), then
// what this session has used in tokens.
func (m *Model) costPanel(u *usageInfo) *item {
	t := m.totals
	_, window := m.agent.ModelInfo()
	rows := [][2]string{
		{"requests", fmt.Sprintf("%d", t.Requests)},
		{"input", fmt.Sprintf("%s tokens (%s cached)", formatTokens(t.InputTokens), formatTokens(t.CachedTokens))},
		{"output", formatTokens(t.OutputTokens) + " tokens"},
	}
	if u == nil {
		rows = append(rows, [2]string{"cost", m.costWithoutAccount()})
	} else {
		rows = append([][2]string{{"#This session", ""}}, rows...)
	}
	if m.context > 0 && window > 0 {
		rows = append(rows, [2]string{"context", fmt.Sprintf("%s of %s (%.0f%%)", formatTokens(m.context), formatTokens(window), 100*float64(m.context)/float64(window))})
	}
	return &item{kind: itemPanel, title: "Usage", usage: u, rows: rows}
}

// costWithoutAccount is /usage's cost row with no account to ask: nobody
// is signed in, or, in a development build only, a local model server
// answers, which costs nothing.
func (m *Model) costWithoutAccount() string {
	if devserver.Enabled && m.cfg.Local() {
		return "none (local model)"
	}
	return "signed out · /login"
}
