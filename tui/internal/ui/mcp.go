package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/justin06lee/caveira/core/mcp"
	"github.com/justin06lee/caveira/core/skills"
)

// /mcp and /skills: lists in a card in the input's place, with the
// actions on keys, and the registry search, the import and skill
// installs running in the background.

// asking is a value the input asks for, such as an MCP server's token.
// What is typed goes to done instead of the model, and is neither shown
// in the transcript nor kept in the history.
type asking struct {
	label string
	done  func(value string) tea.Cmd
}

func (m *Model) askFor(label string, done func(string) tea.Cmd) {
	m.ask = &asking{label: label, done: done}
	m.input.Reset()
	m.input.Placeholder = label
	m.layout()
}

func (m *Model) endAsk() {
	m.ask = nil
	m.input.Placeholder = "Ask caveira to build, fix, or explain…"
	m.input.Reset()
	m.layout()
}

type mcpSearchMsg struct {
	query string
	list  []mcp.Listing
	err   error
}

type skillsInstalledMsg struct {
	src  skills.Source
	list []skills.Skill
	err  error
}

func (m *Model) mcpCommand(args []string) tea.Cmd {
	if m.servers == nil {
		m.push(&item{kind: itemError, text: "MCP servers are not available in this session"})
		return nil
	}
	if len(args) == 0 {
		m.openCard(m.serverCard())
		return m.startTicking()
	}
	switch strings.ToLower(args[0]) {
	case "search", "add":
		q := strings.Join(args[1:], " ")
		if strings.TrimSpace(q) == "" {
			m.push(&item{kind: itemError, text: "/mcp search <words> looks for servers in the MCP Registry"})
			return nil
		}
		m.push(&item{kind: itemNotice, text: "searching the MCP Registry for " + q + "…"})
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			list, err := mcp.Search(ctx, q)
			return mcpSearchMsg{query: q, list: list, err: err}
		}
	case "import":
		found := mcp.Detect(m.workDir)
		if len(found) == 0 {
			m.push(&item{kind: itemNotice, text: "no MCP servers set up in Claude Code, Claude Desktop or Cursor to bring over"})
			return nil
		}
		m.openCard(m.importCard(found))
		return nil
	default:
		for _, s := range m.servers.States() {
			if s.Name == args[0] {
				m.push(serverPanel(s))
				return nil
			}
		}
		m.push(&item{kind: itemError, text: "no MCP server called " + args[0] + " · /mcp lists them"})
	}
	return nil
}

func statusOf(s mcp.State) (string, string) {
	switch s.Status {
	case mcp.Ready:
		if s.Deferred {
			return plural(len(s.Tools), "tool") + " · loaded on demand", "ok"
		}
		return plural(len(s.Tools), "tool"), "ok"
	case mcp.Failed:
		return "failed", "err"
	case mcp.Off:
		return "off", ""
	}
	return "starting…", ""
}

// serverCard is /mcp: every server, how it is, and what can be done.
func (m *Model) serverCard() *card {
	c := &card{
		title: "MCP servers",
		empty: "No MCP servers yet. /mcp search <words> adds one from the MCP Registry; /mcp import brings over the ones set up in Claude Code, Claude Desktop or Cursor.",
		foot:  th.Faint.Render("  /mcp search <words> adds one · /mcp import brings yours from other apps"),
		keys:  [][2]string{{"↑↓", "choose"}, {"enter", "tools"}, {"space", "on/off"}, {"x", "remove"}, {"esc", "close"}},
	}
	fill := func() {
		c.rows = c.rows[:0]
		for _, s := range m.servers.States() {
			status, style := statusOf(s)
			c.rows = append(c.rows, cardRow{id: s.Name, name: s.Name, detail: s.Summary, status: status, statusStyle: style})
		}
	}
	fill()
	c.act = func(k string, row *cardRow) (tea.Cmd, bool) {
		if row == nil {
			return nil, false
		}
		switch k {
		case "enter":
			for _, s := range m.servers.States() {
				if s.Name == row.id {
					m.push(serverPanel(s))
				}
			}
			m.closeCard()
			m.follow = true
			return nil, true
		case "space", " ":
			on := row.status == "off"
			if err := mcp.SetEnabled(m.workDir, row.id, on); err != nil {
				m.push(&item{kind: itemError, text: err.Error()})
				return nil, true
			}
			m.servers.Start()
			fill()
			return m.startTicking(), true
		case "x", "delete", "backspace":
			if c.armed != row.id {
				c.armed = row.id
				return nil, true
			}
			c.armed = ""
			if err := mcp.Remove(m.workDir, row.id); err != nil {
				m.push(&item{kind: itemError, text: err.Error()})
				return nil, true
			}
			m.servers.Start()
			m.push(&item{kind: itemNotice, text: "removed MCP server " + row.id})
			fill()
			return nil, true
		}
		return nil, false
	}
	c.refresh = fill
	return c
}

// serverPanel shows one server: where it is set up, what it runs, why it
// failed, and its tools.
func serverPanel(s mcp.State) *item {
	status, _ := statusOf(s)
	where := shortPath(mcp.UserPath())
	if s.Scope == mcp.Project {
		where = ".caveira/mcp.json in this project"
	}
	rows := [][2]string{{"status", status}, {"runs", s.Summary}, {"set up in", where}}
	if s.Error != "" {
		rows = append(rows, [2]string{"error", s.Error})
	}
	if len(s.Tools) > 0 {
		rows = append(rows, [2]string{"#Tools", ""})
		for _, t := range s.Tools {
			rows = append(rows, [2]string{t.Name, t.Description})
		}
	}
	return &item{kind: itemPanel, title: "MCP · " + s.Name, rows: rows}
}

// searchCard is the registry's answer: enter adds the selected server,
// ←→ choose how it runs when there is more than one way.
func (m *Model) searchCard(q string, list []mcp.Listing) *card {
	c := &card{
		title: "MCP Registry · " + q,
		empty: "Nothing in the MCP Registry matches " + q + " that caveira can run.",
		keys:  [][2]string{{"↑↓", "choose"}, {"←→", "how it runs"}, {"enter", "add"}, {"esc", "close"}},
	}
	way := map[string]int{}
	label := func(l mcp.Listing) string {
		opts := l.Options()
		o := opts[way[l.Name]]
		kind := strings.SplitN(o.Label, " · ", 2)[0]
		if len(opts) > 1 {
			return "‹ " + kind + " ›"
		}
		return kind
	}
	byID := map[string]mcp.Listing{}
	for _, l := range list {
		byID[l.Name] = l
		desc := l.Description
		if l.Title != "" && !strings.Contains(desc, l.Title) {
			desc = l.Title + " · " + desc
		}
		c.rows = append(c.rows, cardRow{id: l.Name, name: l.ShortName(), detail: desc, status: label(l)})
	}
	c.act = func(k string, row *cardRow) (tea.Cmd, bool) {
		if row == nil {
			return nil, false
		}
		l := byID[row.id]
		n := len(l.Options())
		switch k {
		case "left", "h":
			way[l.Name] = (way[l.Name] + n - 1) % n
		case "right", "l":
			way[l.Name] = (way[l.Name] + 1) % n
		case "enter":
			m.closeCard()
			return m.addFromRegistry(l, l.Options()[way[l.Name]]), true
		default:
			return nil, false
		}
		row.status = label(l)
		return nil, true
	}
	return c
}

// addFromRegistry asks for each value the server needs, then adds it.
func (m *Model) addFromRegistry(l mcp.Listing, o mcp.Option) tea.Cmd {
	needs := o.Needs()
	given := map[string]string{}
	var next func(i int) tea.Cmd
	next = func(i int) tea.Cmd {
		if i < len(needs) {
			in := needs[i]
			label := in.Name
			if in.Description != "" {
				label += " — " + oneLine(in.Description, 80)
			}
			if in.IsSecret {
				label += " (kept in ~/.caveira/mcp.json)"
			}
			m.askFor(label, func(v string) tea.Cmd {
				given[in.Name] = v
				return next(i + 1)
			})
			return nil
		}
		return m.finishAdd(l, o, given)
	}
	if len(needs) > 0 {
		m.push(&item{kind: itemNotice, text: fmt.Sprintf("%s needs %s · esc cancels", l.ShortName(), needNames(needs))})
	}
	return next(0)
}

func needNames(needs []mcp.Input) string {
	var names []string
	for _, n := range needs {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}

func (m *Model) finishAdd(l mcp.Listing, o mcp.Option, given map[string]string) tea.Cmd {
	s, err := o.Config(given)
	if err != nil {
		m.push(&item{kind: itemError, text: err.Error()})
		return nil
	}
	name := l.ShortName()
	taken := map[string]bool{}
	for _, st := range m.servers.States() {
		taken[st.Name] = true
	}
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", l.ShortName(), i)
	}
	if err := mcp.Add(mcp.User, m.workDir, name, s); err != nil {
		m.push(&item{kind: itemError, text: err.Error()})
		return nil
	}
	m.servers.Start()
	text := fmt.Sprintf("added MCP server %s (%s) to %s\nit is starting; its tools come with your next message · /mcp to check", name, o.Label, shortPath(mcp.UserPath()))
	m.push(&item{kind: itemNotice, text: text})
	m.follow = true
	return m.startTicking()
}

// importCard offers the servers found in other apps' settings: space
// picks, enter copies the picked ones (or the selected one).
func (m *Model) importCard(found []mcp.Found) *card {
	c := &card{
		title: "Import MCP servers",
		foot:  th.Faint.Render("  nothing is copied until you press enter"),
		keys:  [][2]string{{"↑↓", "choose"}, {"space", "pick"}, {"enter", "copy in"}, {"esc", "cancel"}},
	}
	for i, f := range found {
		status := f.Source
		style := ""
		if f.Have {
			status, style = f.Source+" · replaces yours", "err"
		}
		c.rows = append(c.rows, cardRow{id: fmt.Sprint(i), name: f.Name, detail: f.Server.Summary(), status: status, statusStyle: style, pickable: true})
	}
	c.act = func(k string, row *cardRow) (tea.Cmd, bool) {
		if row == nil {
			return nil, false
		}
		switch k {
		case "space", " ":
			row.picked = !row.picked
			return nil, true
		case "enter":
			var picks []mcp.Found
			for i, r := range c.rows {
				if r.picked {
					picks = append(picks, found[i])
				}
			}
			if len(picks) == 0 {
				picks = []mcp.Found{found[c.sel]}
			}
			m.closeCard()
			n, err := mcp.Import(m.workDir, picks)
			if err != nil {
				m.push(&item{kind: itemError, text: err.Error()})
			}
			if n > 0 {
				var names []string
				for _, p := range picks[:n] {
					names = append(names, p.Name)
				}
				m.servers.Start()
				m.push(&item{kind: itemNotice, text: fmt.Sprintf("copied %s in · starting; their tools come with your next message", strings.Join(names, ", "))})
			}
			return m.startTicking(), true
		}
		return nil, false
	}
	return c
}

// reportFailures says once, after a turn, which MCP servers could not
// start.
func (m *Model) reportFailures() {
	if m.servers == nil {
		return
	}
	if m.reported == nil {
		m.reported = map[string]bool{}
	}
	var failed []string
	for _, s := range m.servers.States() {
		if s.Status == mcp.Failed && !m.reported[s.Name] {
			m.reported[s.Name] = true
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		m.push(&item{kind: itemNotice, text: "MCP server " + strings.Join(failed, ", ") + " could not start · /mcp " + failed[0] + " says why"})
	}
}

// ---- skills ----

func (m *Model) skillsCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		m.openCard(m.skillsCard())
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "install", "add":
		if len(args) < 2 {
			m.push(&item{kind: itemError, text: "/skills install <github url or owner/repo/path>"})
			return nil
		}
		src, err := skills.ParseSource(args[1])
		if err != nil {
			m.push(&item{kind: itemError, text: err.Error()})
			return nil
		}
		m.push(&item{kind: itemNotice, text: "installing skills from " + src.String() + "…"})
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			list, err := skills.Install(ctx, src)
			return skillsInstalledMsg{src: src, list: list, err: err}
		}
	}
	m.push(&item{kind: itemError, text: "/skills lists them; /skills install <github url> adds some"})
	return nil
}

func (m *Model) skillsInstalled(msg skillsInstalledMsg) {
	if msg.err != nil {
		m.push(&item{kind: itemError, text: "could not install from " + msg.src.String() + ": " + msg.err.Error()})
	}
	if len(msg.list) > 0 {
		var names []string
		for _, s := range msg.list {
			names = append(names, s.Name)
		}
		m.refreshSkills()
		m.push(&item{kind: itemNotice, text: fmt.Sprintf("installed %s in %s · in use from your next message", strings.Join(names, ", "), shortPath(skills.InstallDir()))})
	}
	m.follow = true
}

// refreshSkills puts the skills that are on now in the system prompt and
// the tools. A turn that is running keeps the old ones.
func (m *Model) refreshSkills() {
	if m.agent == nil {
		return
	}
	if m.running {
		m.reskill = true
		return
	}
	skills.Use(m.agent.Tools, m.workDir)
	if m.system != nil {
		m.agent.System = m.system()
	}
}

func (m *Model) skillsCard() *card {
	c := &card{
		title: "Skills",
		empty: "No skills yet. /skills install <github url or owner/repo/path> adds some; skills in ~/.claude/skills and the project's .claude/skills are picked up as they are.",
		foot:  th.Faint.Render("  /skills install <github url or owner/repo/path> adds more"),
		keys:  [][2]string{{"↑↓", "choose"}, {"enter", "details"}, {"space", "on/off"}, {"x", "remove"}, {"esc", "close"}},
	}
	var list []skills.Skill
	fill := func() {
		list = skills.Find(m.workDir)
		c.rows = c.rows[:0]
		for _, s := range list {
			status, style := s.Where, "ok"
			if !s.Enabled {
				status, style = "off", ""
			}
			c.rows = append(c.rows, cardRow{id: s.Name, name: s.Name, detail: s.Description, status: status, statusStyle: style})
		}
	}
	fill()
	c.act = func(k string, row *cardRow) (tea.Cmd, bool) {
		if row == nil {
			return nil, false
		}
		var s skills.Skill
		for _, x := range list {
			if x.Name == row.id {
				s = x
			}
		}
		switch k {
		case "enter":
			status := "on"
			if !s.Enabled {
				status = "off"
			}
			m.push(&item{kind: itemPanel, title: "Skill · " + s.Name, rows: [][2]string{
				{"status", status}, {"what", s.Description}, {"folder", shortPath(s.Dir)},
			}})
			m.closeCard()
			m.follow = true
			return nil, true
		case "space", " ":
			if err := skills.SetEnabled(s.Name, !s.Enabled); err != nil {
				m.push(&item{kind: itemError, text: err.Error()})
				return nil, true
			}
			m.refreshSkills()
			fill()
			return nil, true
		case "x", "delete", "backspace":
			if !s.Installed {
				c.armed = ""
				_, cmd := m.flash(s.Name + " is in " + s.Where + "; space turns it off")
				return cmd, true
			}
			if c.armed != row.id {
				c.armed = row.id
				return nil, true
			}
			c.armed = ""
			if err := skills.Remove(m.workDir, s.Name); err != nil {
				m.push(&item{kind: itemError, text: err.Error()})
				return nil, true
			}
			m.refreshSkills()
			m.push(&item{kind: itemNotice, text: "removed skill " + s.Name})
			fill()
			return nil, true
		}
		return nil, false
	}
	return c
}
