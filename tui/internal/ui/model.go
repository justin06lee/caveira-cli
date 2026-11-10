// Package ui is the terminal session: the skull and the prompt while things
// load and until the first message, then a scrolling transcript with the
// input at the bottom, all driven by events from the agent.
package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
	"github.com/justin06lee/caveira/core/mcp"
	"github.com/justin06lee/caveira/core/tools"
)

// Options is everything the session needs once startup work is done.
type Options struct {
	// Available is a newer release this launch found and did not install.
	Available string
	Agent     *agent.Agent
	Settings  config.Settings
	WorkDir   string
	// Branch is the git branch checked out in WorkDir, if any.
	Branch string
	// ListModels fills the /model picker. Nil asks the agent's endpoint.
	ListModels func(context.Context) ([]ModelChoice, error)
	Version    string
	// Initial, when set, is sent as the first message.
	Initial string
	// Resumed, when set, is the session that was picked up.
	Resumed *agent.Session
	// Fatal, when set, disables sending and explains why.
	Fatal error
	// Reload resolves the settings again, the way startup did, after
	// /login or /logout. Nil reads them with config.Load.
	Reload func() (config.Settings, error)
	// MCP is the session's MCP servers, for /mcp; nil has none.
	MCP *mcp.Manager
	// System builds the system prompt again, after /skills changes
	// which skills are on. Nil keeps the one the agent has.
	System func() string
}

type phase int

const (
	phaseSplash  phase = iota // the intro plays while boot runs
	phaseHero                 // empty session: skull, centred prompt
	phaseSlide                // first prompt sent: box sliding down
	phaseSession              // transcript and bottom prompt
)

type Model struct {
	phase   phase
	boot    func() (Options, error)
	booted  bool
	bootErr error
	splash  splash

	agent   *agent.Agent
	cfg     config.Settings
	workDir string
	branch  string
	version string

	width, height int
	vp            viewport.Model
	input         textarea.Model
	rend          renderer

	// what the terminal told us about itself
	dark    bool
	bg      color.Color
	profile colorprofile.Profile

	items    []*item
	targets  []copyTarget // the copy buttons in the transcript, by line
	dirty    bool
	follow   bool
	pendingR *item // in-progress reasoning block
	pendingA *item // in-progress assistant block

	running    bool
	compacting bool
	cancel     context.CancelFunc
	events     chan agent.Event
	approval   *agent.ApprovalEvent
	apSel      int
	picker     *modelPicker
	listModels func(context.Context) ([]ModelChoice, error)
	// queued is what you sent while the model was busy, oldest first; it
	// goes out together when the turn ends.
	queued     []string
	turnStart  time.Time
	turnTokens int // tokens used before this turn
	turnTools  int
	turnFailed bool
	// retry is the busy model being waited out, if it is; canContinue
	// says the last turn gave up waiting, and enter takes it up again.
	retry       *agent.RetryEvent
	canContinue bool
	verb        string // what the status line says the model is up to
	streamed    int    // characters streamed this turn, for the token estimate

	frame   int
	ticking bool

	totals  agent.Totals
	context int

	// usageSeen is the account's share of its period used, as the site
	// last said; usageKnown says it has said.
	usageSeen  float64
	usageKnown bool
	notice     string
	fatal      error

	// account is who requests go out as when they go through a caveira
	// account, "" when they do not.
	account string
	// login is a /login waiting on the browser, if one is.
	login *pendingLogin
	// tokenRejected says the account's token came back unauthorized, so
	// /login signs in again rather than saying who is signed in.
	tokenRejected bool
	// reconnect holds a new endpoint or key that came mid-turn until the
	// turn ends.
	reconnect bool
	reload    func() (config.Settings, error)

	history []string
	histPos int
	draft   string
	palSel  int

	quitArmed time.Time
	initial   string

	// newer is a release newer than this one, once a check has found it;
	// updating says /update is downloading it.
	newer    string
	updating bool

	// servers is the session's MCP servers and system rebuilds the
	// prompt (Options). card is /mcp's or /skills' list in the input's
	// place, and ask a value being asked for in the input (mcp.go).
	servers  *mcp.Manager
	system   func() string
	card     *card
	ask      *asking
	reported map[string]bool // MCP servers whose failure has been said
	// reskill says skills changed during a turn; the prompt and tools
	// follow when it ends.
	reskill bool

	slideT    float64
	slideText string
}

type eventMsg struct{ ev agent.Event }
type eventsClosedMsg struct{}
type bootMsg struct {
	opts Options
	err  error
}
type shellDoneMsg struct {
	it     *item
	result tools.Result
}
type clearNoticeMsg struct{}
type tickMsg struct{}

// Run shows the intro immediately and calls boot on another goroutine;
// the session starts when both are done. Errors from boot end the program.
func Run(version string, boot func() (Options, error)) error {
	m := New(version, boot)
	_, err := tea.NewProgram(m).Run()
	if m.servers != nil {
		m.servers.Close()
	}
	if err != nil {
		return err
	}
	return m.bootErr
}

// New builds the screen. boot may be nil when the caller applies Options
// itself with Apply.
func New(version string, boot func() (Options, error)) *Model {
	ta := textarea.New()
	ta.Placeholder = "Ask caveira to build, fix, or explain…"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	// MaxHeight is how tall the box grows; past that the text scrolls
	// inside it. Without MaxContentHeight the textarea would refuse new
	// lines at MaxHeight instead.
	ta.MaxHeight = 8
	ta.MaxContentHeight = 10_000
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j", "shift+enter"))
	ta.SetVirtualCursor(false)
	ta.Focus()

	vp := viewport.New()
	vp.SoftWrap = false
	vp.MouseWheelEnabled = true
	vp.KeyMap = viewport.KeyMap{}

	m := &Model{
		phase:   phaseSplash,
		boot:    boot,
		version: version,
		input:   ta,
		vp:      vp,
		follow:  true,
		dark:    true,
		profile: colorprofile.TrueColor,
	}
	m.applyTheme()
	if boot == nil {
		m.booted = true
	}
	return m
}

// applyTheme rebuilds the palette from what the terminal has reported and
// re-renders everything drawn with the old one.
func (m *Model) applyTheme() {
	th = newTheme(m.dark, m.profile, m.bg)
	st := textarea.DefaultDarkStyles()
	// The prompt box is a shaded panel where the terminal can shade, and the
	// text area draws on the panel's colour so nothing in it shows through.
	plain, text, faint := lipgloss.NewStyle(), th.Text, th.Faint
	if th.surface != nil {
		plain = plain.Background(th.surface)
		text, faint = text.Background(th.surface), faint.Background(th.surface)
	}
	st.Focused.Base = plain
	st.Focused.CursorLine = plain
	st.Focused.Text = text
	st.Focused.Placeholder = faint
	st.Focused.EndOfBuffer = plain
	st.Blurred = st.Focused
	st.Cursor.Color = th.accent
	m.input.SetStyles(st)
	m.rend.md = nil // rebuilt at the current width by layout
	for _, it := range m.items {
		it.invalidate()
	}
	m.dirty = true
	m.layout()
}

// Apply installs what boot produced.
func (m *Model) Apply(o Options) {
	m.agent = o.Agent
	m.cfg = o.Settings
	m.workDir = o.WorkDir
	m.branch = o.Branch
	m.listModels = o.ListModels
	if o.Version != "" {
		m.version = o.Version
		if o.Available != "" {
			m.newer = o.Available
		}
	}
	m.fatal = o.Fatal
	m.reload = o.Reload
	m.servers = o.MCP
	m.system = o.System
	m.account = accountName(o.Settings)
	m.initial = o.Initial
	if o.Agent != nil {
		m.totals = o.Agent.Totals
	}
	if o.Resumed != nil {
		m.pushHeader()
		m.replay(o.Resumed)
		if m.signedOut() {
			m.push(&item{kind: itemNotice, text: signInHint})
		}
	}
	m.booted = true
}

// useSettings reads the settings again and points the session at what
// they say now, after /login or /logout: the endpoint and the key change,
// while the conversation, the model, and the effort stay as they are.
func (m *Model) useSettings() {
	reload := m.reload
	if reload == nil {
		reload = func() (config.Settings, error) { return config.Load(m.workDir) }
	}
	cfg, err := reload()
	m.cfg.BaseURL, m.cfg.APIKey, m.cfg.KeySource = cfg.BaseURL, cfg.APIKey, cfg.KeySource
	m.fatal = err
	m.tokenRejected = false
	m.account = accountName(m.cfg)
	if len(m.items) > 0 && m.items[0].kind == itemHeader {
		m.items[0].header.account = m.account
		m.items[0].invalidate()
		m.dirty = true
	}
	if m.running {
		// The turn is using the client; the new one waits for it to end.
		m.reconnect = true
		return
	}
	m.connect()
}

// connect gives the agent a client for the current endpoint and key,
// keeping the headers the session already sends.
func (m *Model) connect() {
	if m.agent == nil {
		return
	}
	c := llm.New(m.cfg.BaseURL, m.cfg.APIKey)
	if old := m.agent.Client; old != nil {
		maps.Copy(c.Headers, old.Headers)
	}
	m.agent.Client = c
}

// accountName is who requests go out as when s sends them through a
// caveira account: its email, or "" when s does not.
func accountName(s config.Settings) string {
	if !s.OnAccount() {
		return ""
	}
	if a, ok := cloud.Load(); ok && a.Email != "" {
		return a.Email
	}
	return "caveira account"
}

// replay rebuilds the transcript from a stored session.
func (m *Model) replay(s *agent.Session) {
	byID := map[string]*item{}
	for _, msg := range s.History() {
		switch msg.Role {
		case llm.RoleUser:
			if strings.HasPrefix(msg.Content, "This session's earlier conversation was compacted.") {
				m.push(&item{kind: itemDivider, text: "context compacted"})
				continue
			}
			if strings.HasPrefix(msg.Content, "I ran this command myself") {
				continue
			}
			m.push(&item{kind: itemUser, text: msg.Content})
		case llm.RoleAssistant:
			if strings.HasPrefix(msg.Content, "Understood. I have the state from the handoff note") {
				continue
			}
			if msg.Reasoning != "" {
				m.push(&item{kind: itemReasoning, text: msg.Reasoning})
			}
			if strings.TrimSpace(msg.Content) != "" {
				m.push(&item{kind: itemAssistant, text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				it := &item{kind: itemTool, toolID: tc.ID, toolName: tc.Function.Name}
				if t, ok := m.agent.Tools.Get(tc.Function.Name); ok {
					it.preview = t.Preview([]byte(tc.Function.Arguments))
					it.toolKind = t.Kind()
				}
				byID[tc.ID] = it
				m.push(it)
			}
		case llm.RoleTool:
			if it, ok := byID[msg.ToolCallID]; ok {
				it.result = &tools.Result{Output: msg.Content, Summary: "from the previous session", IsError: strings.HasPrefix(msg.Content, "Error:")}
			}
		}
	}
	m.push(&item{kind: itemDivider, text: "resumed " + s.ID})
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{splashTicker(), tea.RequestBackgroundColor, checkUpdate(m.version), updateTick()}
	if m.boot != nil {
		boot := m.boot
		cmds = append(cmds, func() tea.Msg {
			o, err := boot()
			return bootMsg{opts: o, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) push(it *item) {
	m.items = append(m.items, it)
	m.dirty = true
}

// drop takes items out of the transcript.
func (m *Model) drop(its ...*item) {
	kept := m.items[:0]
	for _, it := range m.items {
		if !slices.Contains(its, it) {
			kept = append(kept, it)
		}
	}
	clear(m.items[len(kept):])
	m.items = kept
	m.dirty = true
}

// ---- update ----

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.bg = msg.Color
		m.applyTheme()
		return m, nil

	case tea.ColorProfileMsg:
		m.profile = msg.Profile
		m.applyTheme()
		return m, nil

	case bootMsg:
		if msg.err != nil {
			m.bootErr = msg.err
			m.booted = true
		} else {
			m.Apply(msg.opts)
		}
		if m.phase == phaseSplash && m.splash.frame >= bootFrames {
			next, cmd := m.leaveSplash()
			return next, tea.Batch(cmd, m.watchUsage())
		}
		// Where usage stands before the first reply, so the reply that
		// takes it past 80% is the one that says so.
		return m, m.watchUsage()
	}

	switch m.phase {
	case phaseSplash:
		switch msg.(type) {
		case splashTickMsg, tea.KeyPressMsg:
			return m.updateSplash(msg)
		}
	case phaseSlide:
		switch msg.(type) {
		case slideTickMsg, tea.KeyPressMsg:
			return m.updateSlide(msg)
		}
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		if m.phase != phaseSession {
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case tea.MouseClickMsg:
		if m.phase != phaseSession {
			return m, nil
		}
		return m, m.click(msg.Mouse())

	case copiedDoneMsg:
		m.copiedDone(msg)
		return m, nil

	case tickMsg:
		if !m.running && !m.hasRunningTool() && (m.picker == nil || !m.picker.loading) && m.login == nil && (m.card == nil || m.card.refresh == nil) {
			m.ticking = false
			return m, nil
		}
		m.frame++
		m.rend.frame = m.frame
		m.dirty = true
		return m, tick()

	case eventMsg:
		return m.handleEvent(msg.ev)

	case eventsClosedMsg:
		return m.turnEnded()

	case pickerModelsMsg:
		if m.picker == nil {
			return m, nil
		}
		m.picker.loading = false
		m.picker.err = msg.err
		if len(msg.choices) > 0 {
			m.picker.setChoices(msg.choices, m.agent.Model)
		}
		m.layout()
		return m, nil

	case shellDoneMsg:
		msg.it.running = false
		msg.it.result = &msg.result
		msg.it.elapsed = time.Since(msg.it.started)
		msg.it.invalidate()
		m.dirty = true
		if m.agent != nil {
			m.agent.Note(fmt.Sprintf("I ran this command myself in the working directory:\n\n$ %s\n\n%s", msg.it.preview, msg.result.Output))
		}
		return m, nil

	case clearNoticeMsg:
		m.notice = ""
		return m, nil

	case loginCodeMsg:
		return m, m.loginCode(msg)

	case loginDoneMsg:
		m.loginDone(msg)
		return m, m.watchUsage()

	case accountMsg:
		m.accountAnswered(msg)
		return m, nil

	case logoutDoneMsg:
		m.logoutDone(msg)
		return m, nil

	case updateTickMsg:
		return m, tea.Batch(checkUpdate(m.version), updateTick())
	case updateAvailableMsg:
		m.updateAvailable(msg)
		return m, nil

	case updateDoneMsg:
		m.updateDone(msg)
		return m, nil

	case mcpSearchMsg:
		if msg.err != nil {
			m.push(&item{kind: itemError, text: "could not search the MCP Registry: " + msg.err.Error()})
			return m, nil
		}
		m.openCard(m.searchCard(msg.query, msg.list))
		return m, nil

	case skillsInstalledMsg:
		m.skillsInstalled(msg)
		return m, nil
	}

	// Anything else (paste, blink, cursor) belongs to the textarea.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return m, cmd
}

func tick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// startTicking runs the animation clock (spinner, shimmer, timers) until
// nothing is running.
func (m *Model) startTicking() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tick()
}

func (m *Model) hasRunningTool() bool {
	for i := len(m.items) - 1; i >= 0 && i >= len(m.items)-5; i-- {
		if m.items[i].kind == itemTool && m.items[i].running {
			return true
		}
	}
	return false
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()

	if m.approval != nil {
		switch k {
		case "up", "k":
			m.apSel = (m.apSel + len(approvalChoices) - 1) % len(approvalChoices)
		case "down", "j", "tab":
			m.apSel = (m.apSel + 1) % len(approvalChoices)
		case "enter":
			return m.decide([]agent.Decision{agent.Allow, agent.AllowAlways, agent.Deny}[m.apSel])
		case "1", "y", "Y":
			return m.decide(agent.Allow)
		case "2", "a", "A":
			return m.decide(agent.AllowAlways)
		case "3", "n", "N", "esc", "ctrl+c":
			return m.decide(agent.Deny)
		}
		return m, nil
	}

	if m.picker != nil {
		return m.pickerKey(k)
	}

	if m.card != nil {
		return m.cardKey(k)
	}

	if m.ask != nil && k == "esc" {
		m.endAsk()
		m.push(&item{kind: itemNotice, text: "cancelled"})
		return m, nil
	}

	if matches := m.paletteMatches(); len(matches) > 0 {
		sel := matches[m.palSel%len(matches)]
		switch k {
		case "up":
			m.palSel = (m.palSel + len(matches) - 1) % len(matches)
			return m, nil
		case "down":
			m.palSel = (m.palSel + 1) % len(matches)
			return m, nil
		case "tab":
			v := sel.name
			if sel.args != "" {
				v += " "
			}
			m.input.SetValue(v)
			m.input.MoveToEnd()
			m.palSel = 0
			m.layout()
			return m, nil
		case "enter":
			if sel.args != "" && m.input.Value() != sel.name && !isAlias(sel, m.input.Value()) {
				// Commands that take arguments complete first, so the
				// argument can be typed.
				m.input.SetValue(sel.name + " ")
				m.input.MoveToEnd()
				m.palSel = 0
				m.layout()
				return m, nil
			}
			m.input.SetValue(sel.name)
		}
	}

	switch k {
	case "ctrl+c":
		if m.running {
			m.interrupt()
			return m, nil
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.layout()
			return m, nil
		}
		if time.Since(m.quitArmed) < 2*time.Second {
			return m, tea.Quit
		}
		m.quitArmed = time.Now()
		return m.flash("press ctrl+c again to quit")

	case "ctrl+d":
		if m.input.Value() == "" && !m.running {
			return m, tea.Quit
		}

	case "esc":
		if m.running {
			m.interrupt()
			return m, nil
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.layout()
		} else if m.login != nil {
			m.cancelLogin()
		}
		return m, nil

	case "ctrl+t":
		m.rend.showReasoning = !m.rend.showReasoning
		m.dirty = true
		if m.rend.showReasoning {
			return m.flash("showing thinking")
		}
		return m.flash("hiding thinking")

	case "ctrl+o":
		m.rend.expandTools = !m.rend.expandTools
		m.dirty = true
		if m.rend.expandTools {
			return m.flash("tool output expanded")
		}
		return m.flash("tool output collapsed")

	case "pgup":
		m.vp.PageUp()
		m.follow = false
		return m, nil
	case "pgdown":
		m.vp.PageDown()
		m.follow = m.vp.AtBottom()
		return m, nil
	case "ctrl+home":
		m.vp.GotoTop()
		m.follow = false
		return m, nil
	case "ctrl+end":
		m.vp.GotoBottom()
		m.follow = true
		return m, nil

	case "up":
		if n := len(m.queued); n > 0 && m.input.Value() == "" {
			// Take the newest queued message back to change it.
			m.input.SetValue(m.queued[n-1])
			m.input.MoveToEnd()
			m.queued = m.queued[:n-1]
			m.dirty = true
			m.layout()
			return m, nil
		}
		if m.input.LineCount() <= 1 && len(m.history) > 0 && (m.input.Value() == "" || m.histPos < len(m.history)) {
			if m.histPos == len(m.history) {
				m.draft = m.input.Value()
			}
			if m.histPos > 0 {
				m.histPos--
			}
			m.input.SetValue(m.history[m.histPos])
			m.input.MoveToEnd()
			m.layout()
			return m, nil
		}
	case "down":
		if m.histPos < len(m.history) && m.input.LineCount() <= 1 {
			m.histPos++
			if m.histPos == len(m.history) {
				m.input.SetValue(m.draft)
			} else {
				m.input.SetValue(m.history[m.histPos])
			}
			m.input.MoveToEnd()
			m.layout()
			return m, nil
		}

	case "enter":
		text := m.input.Value()
		if strings.HasSuffix(text, "\\") {
			// A trailing backslash asks for a newline, like a shell.
			m.input.SetValue(strings.TrimSuffix(text, "\\") + "\n")
			m.input.MoveToEnd()
			m.layout()
			return m, nil
		}
		text = strings.TrimSpace(text)
		if text == "" {
			if m.ask == nil && m.continuable() && !m.signedOut() {
				return m, m.continueTurn()
			}
			return m, nil
		}
		if a := m.ask; a != nil {
			// An answer to a question, kept out of the history and the
			// transcript: it can be a token.
			m.endAsk()
			return m, a.done(text)
		}
		m.input.Reset()
		m.palSel = 0
		m.layout()
		m.history = append(m.history, text)
		m.histPos = len(m.history)
		m.draft = ""
		return m, m.submit(text)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.palSel = 0
	m.layout()
	return m, cmd
}

func isAlias(c command, v string) bool {
	for _, a := range c.aliases {
		if a == v {
			return true
		}
	}
	return false
}

func (m *Model) flash(s string) (tea.Model, tea.Cmd) {
	m.notice = s
	return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearNoticeMsg{} })
}

func (m *Model) decide(d agent.Decision) (tea.Model, tea.Cmd) {
	if m.approval == nil {
		return m, nil
	}
	m.approval.Reply <- d
	m.approval = nil
	m.apSel = 0
	m.layout()
	return m, nil
}

func (m *Model) interrupt() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.approval != nil {
		// The agent is waiting on us; a cancelled context releases it.
		m.approval = nil
		m.layout()
	}
	m.notice = "interrupting…"
}

// submit handles slash commands and shell escapes locally, and sends
// everything else to the agent. From the home screen, the first submission
// plays the slide and comes back here once the box has landed.
func (m *Model) submit(text string) tea.Cmd {
	if m.phase == phaseHero {
		return m.beginSlide(text)
	}
	if strings.HasPrefix(text, "/") {
		return m.slash(text)
	}
	if strings.HasPrefix(text, "!") && len(text) > 1 {
		return m.shell(strings.TrimSpace(text[1:]))
	}
	if m.fatal != nil {
		m.push(&item{kind: itemError, text: m.fatal.Error()})
		return nil
	}
	if m.signedOut() {
		m.push(&item{kind: itemError, text: signInHint})
		return nil
	}
	if m.running {
		m.queued = append(m.queued, text)
		m.follow = true
		m.dirty = true
		return nil
	}
	return m.startTurn(text)
}

// startTurn sends one or more messages as a single turn: each gets its own
// bubble and its own place in the history, and the model answers them
// together.
func (m *Model) startTurn(texts ...string) tea.Cmd {
	for _, t := range texts {
		m.push(&item{kind: itemUser, text: t})
	}
	m.beginTurn()

	if m.agent.Session == nil {
		m.agent.NewSession()
	}
	for _, t := range texts[:len(texts)-1] {
		m.agent.Note(t)
	}
	text := texts[len(texts)-1]
	return m.launch(func(ctx context.Context, emit func(agent.Event)) { m.agent.Run(ctx, text, emit) })
}

// continueTurn takes up the turn that gave up waiting out a busy model.
func (m *Model) continueTurn() tea.Cmd {
	m.beginTurn()
	return m.launch(m.agent.Continue)
}

// continuable says enter would take up the last turn again.
func (m *Model) continuable() bool {
	return m.canContinue && !m.running && m.agent != nil && m.agent.CanContinue()
}

func (m *Model) beginTurn() {
	m.follow = true
	m.running = true
	m.notice = ""
	m.pendingA, m.pendingR = nil, nil
	m.turnStart = time.Now()
	m.turnTokens = m.totals.InputTokens + m.totals.OutputTokens
	m.turnTools = 0
	m.turnFailed = false
	m.retry, m.canContinue = nil, false
	m.streamed = 0
	m.verb = pickVerb(m.verb)
}

// launch runs a turn on its own goroutine, its events coming back as
// messages.
func (m *Model) launch(run func(ctx context.Context, emit func(agent.Event))) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ch := make(chan agent.Event, 256)
	m.events = ch
	go func() {
		defer close(ch)
		run(ctx, func(ev agent.Event) {
			select {
			case ch <- ev:
			case <-ctx.Done():
				// Still deliver, but never block forever on a UI that gave up.
				select {
				case ch <- ev:
				case <-time.After(time.Second):
				}
			}
		})
	}()
	return tea.Batch(waitEvent(ch), m.startTicking())
}

func waitEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg{ev: ev}
	}
}

// endThinking closes the reasoning block when the model moves on, so it
// can say how long it thought.
func (m *Model) endThinking() {
	if r := m.pendingR; r != nil && r.running {
		r.running = false
		r.elapsed = time.Since(r.started)
		r.invalidate()
	}
}

func (m *Model) handleEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	next := waitEvent(m.events)
	if _, ok := ev.(agent.RetryEvent); !ok {
		m.retry = nil
	}
	switch ev := ev.(type) {
	case agent.ReasoningEvent:
		if m.pendingR == nil {
			m.pendingR = &item{kind: itemReasoning, running: true, started: time.Now()}
			m.push(m.pendingR)
		}
		m.pendingR.text += ev.Delta
		m.streamed += len(ev.Delta)
		m.pendingR.invalidate()
		m.dirty = true

	case agent.TextEvent:
		m.endThinking()
		if m.pendingA == nil {
			m.pendingA = &item{kind: itemAssistant, running: true}
			m.push(m.pendingA)
		}
		m.pendingA.text += ev.Delta
		m.streamed += len(ev.Delta)
		m.pendingA.invalidate()
		m.dirty = true

	case agent.AssistantDoneEvent:
		m.endThinking()
		if m.pendingA != nil {
			m.pendingA.text = ev.Message.Content
			m.pendingA.running = false
			m.pendingA.invalidate()
		}
		m.pendingA, m.pendingR = nil, nil
		m.dirty = true

	case agent.ToolStartEvent:
		m.endThinking()
		m.turnTools++
		m.push(&item{
			kind: itemTool, toolID: ev.ID, toolName: ev.Name, preview: ev.Preview,
			toolKind: ev.Kind, running: true, started: time.Now(),
		})

	case agent.ToolEndEvent:
		for i := len(m.items) - 1; i >= 0; i-- {
			it := m.items[i]
			if it.kind == itemTool && it.toolID == ev.ID {
				it.running = false
				res := ev.Result
				it.result = &res
				it.elapsed = ev.Duration
				it.invalidate()
				break
			}
		}
		// The model goes back to work on the result: a new verb for it.
		m.verb = pickVerb(m.verb)
		m.dirty = true

	case agent.ApprovalEvent:
		e := ev
		m.approval = &e
		m.apSel = 0
		m.layout()

	case agent.UsageEvent:
		m.totals = ev.Totals
		m.context = ev.ContextTokens

	case agent.CompactingEvent:
		m.push(&item{kind: itemDivider, text: ev.Text()})

	case agent.CompactEvent:
		m.push(&item{kind: itemDivider, text: ev.Text()})

	case agent.RetryEvent:
		// The reply that was streaming failed; it comes again from the
		// start, so what showed of it goes.
		m.drop(m.pendingA, m.pendingR)
		m.pendingA, m.pendingR = nil, nil
		e := ev
		m.retry = &e
		m.dirty = true

	case agent.ErrorEvent:
		m.turnFailed = true
		text := ev.Err.Error()
		var gaveUp *agent.GaveUpError
		if errors.As(ev.Err, &gaveUp) {
			m.canContinue = true
			text += " · enter to try again"
		}
		var apiErr *llm.APIError
		if m.cfg.OnAccount() && errors.As(ev.Err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			// The account's token is no good any more and /login gets a
			// new one. The site's message says so; in case it does not:
			m.tokenRejected = true
			if !strings.Contains(text, "/login") {
				text += " · /login to sign in again"
			}
		}
		m.push(&item{kind: itemError, text: text})

	case agent.DoneEvent:
		if ev.Interrupted {
			m.push(&item{kind: itemDivider, text: "interrupted · tell caveira what to do instead", warn: true})
		} else if !m.compacting && !m.turnFailed {
			m.push(&item{kind: itemDivider, quiet: true, text: m.turnSummary()})
		}
	}
	return m, next
}

// turnSummary is the line that closes a turn: how long it took, how many
// tools it ran, and the tokens it used. It is never in dollars.
func (m *Model) turnSummary() string {
	parts := []string{"worked for " + formatDuration(max(time.Since(m.turnStart).Truncate(time.Second), time.Second))}
	switch m.turnTools {
	case 0:
	case 1:
		parts = append(parts, "1 tool call")
	default:
		parts = append(parts, fmt.Sprintf("%d tool calls", m.turnTools))
	}
	if d := m.totals.InputTokens + m.totals.OutputTokens - m.turnTokens; d > 0 {
		parts = append(parts, formatTokens(d)+" tokens")
	}
	return strings.Join(parts, " · ")
}

func (m *Model) turnEnded() (tea.Model, tea.Cmd) {
	m.running = false
	m.retry = nil
	m.compacting = false
	m.cancel = nil
	m.approval = nil
	m.notice = ""
	m.endThinking()
	for _, it := range m.items {
		if it.kind == itemTool && it.running {
			it.running = false
			it.result = &tools.Result{Summary: "cancelled", IsError: true}
			it.invalidate()
		}
	}
	if m.pendingA != nil {
		m.pendingA.running = false
		m.pendingA.invalidate()
	}
	m.pendingA, m.pendingR = nil, nil
	m.dirty = true
	if m.agent != nil {
		m.totals = m.agent.Totals
	}
	if m.reconnect {
		m.reconnect = false
		m.connect()
	}
	if m.reskill {
		m.reskill = false
		m.refreshSkills()
	}
	m.reportFailures()
	m.layout()
	usage := m.watchUsage()
	if len(m.queued) > 0 {
		texts := m.queued
		m.queued = nil
		return m, tea.Batch(m.startTurn(texts...), usage)
	}
	return m, usage
}

// shell runs a `!command` typed by the user, shown like a tool call and
// recorded in the conversation so the model knows what happened.
func (m *Model) shell(command string) tea.Cmd {
	it := &item{kind: itemTool, toolName: "bash", preview: command, toolKind: tools.KindExecute, running: true, started: time.Now(), user: true}
	m.push(it)
	m.follow = true
	reg := m.agent.Tools
	return tea.Batch(m.startTicking(), func() tea.Msg {
		args := fmt.Sprintf(`{"command": %q}`, command)
		return shellDoneMsg{it: it, result: reg.Run(context.Background(), "bash", []byte(args))}
	})
}
