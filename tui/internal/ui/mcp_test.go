package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/mcp"
	"github.com/justin06lee/caveira/core/prompt"
	"github.com/justin06lee/caveira/core/skills"
	"github.com/justin06lee/caveira/core/tools"
)

func press(m *Model, k string) {
	switch k {
	case "enter":
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "esc":
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	case "space":
		m.handleKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	case "down":
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	default:
		m.handleKey(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mcpSession is a session in a project of its own with MCP servers from
// ~/.caveira/mcp.json.
func mcpSession(t *testing.T, config string) *Model {
	t.Helper()
	m := testModel(t, 100, 40)
	m.workDir = t.TempDir()
	m.phase = phaseSession
	m.pushHeader()
	if config != "" {
		writeFile(t, mcp.UserPath(), config)
	}
	m.servers = mcp.NewManager(m.workDir, "test")
	m.servers.Start()
	m.servers.Wait(t.Context(), 10*time.Second)
	t.Cleanup(m.servers.Close)
	return m
}

func cardText(m *Model) string { return plain(m.renderCard()) }

func fits(t *testing.T, m *Model, s string) {
	t.Helper()
	for i, l := range strings.Split(s, "\n") {
		if w := lipgloss.Width(l); w > m.inner() {
			t.Fatalf("row %d is %d wide in %d: %q", i, w, m.inner(), l)
		}
	}
}

// /mcp lists the servers with how they are; enter shows one, space turns
// it off and on, x twice removes it.
func TestMCPCardListsAndManagesServers(t *testing.T) {
	m := mcpSession(t, `{"mcpServers":{
		"broken":{"command":"/nonexistent/caveira-test-server"},
		"later":{"command":"npx","args":["-y","something"],"disabled":true}}}`)
	m.slash("/mcp")
	if m.card == nil {
		t.Fatal("no card")
	}
	text := cardText(m)
	fits(t, m, m.renderCard())
	if !strings.Contains(text, "MCP servers") || !strings.Contains(text, "broken") || !strings.Contains(text, "failed") ||
		!strings.Contains(text, "npx -y something") || !strings.Contains(text, "off") {
		t.Fatalf("card:\n%s", text)
	}
	if f := plain(m.renderFooter(m.inner())); !strings.Contains(f, "space on/off") {
		t.Fatalf("footer: %s", f)
	}

	press(m, "enter")
	if m.card != nil {
		t.Fatal("enter left the card open")
	}
	p := last(m)
	if p.kind != itemPanel || p.title != "MCP · broken" || !strings.Contains(rowsText(p), "nonexistent") {
		t.Fatalf("panel %+v", p)
	}

	m.slash("/mcp")
	press(m, "down")
	press(m, "space")
	f, _ := mcp.ReadFile(mcp.UserPath())
	if f.Servers["later"].Disabled {
		t.Fatal("space did not turn later on")
	}
	press(m, "x")
	if !strings.Contains(cardText(m), "x again to remove") {
		t.Fatal("x removed without asking twice")
	}
	press(m, "x")
	f, _ = mcp.ReadFile(mcp.UserPath())
	if _, ok := f.Servers["later"]; ok {
		t.Fatal("later is still set up")
	}
	press(m, "esc")
	if m.card != nil {
		t.Fatal("esc left the card open")
	}

	// The failure is said once, after a turn.
	m.reportFailures()
	m.reportFailures()
	n := 0
	for _, it := range m.items {
		if strings.Contains(it.text, "could not start") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("failure said %d times", n)
	}
}

func rowsText(it *item) string {
	var sb strings.Builder
	for _, r := range it.rows {
		sb.WriteString(r[0] + " " + r[1] + "\n")
	}
	return sb.String()
}

// /mcp import shows what other apps have and copies only what is picked.
func TestMCPImportCopiesWhatIsPicked(t *testing.T) {
	m := mcpSession(t, "")
	home := os.Getenv("HOME")
	writeFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"a"},"two":{"command":"b"}}}`)
	m.slash("/mcp import")
	if m.card == nil || !strings.Contains(cardText(m), "Cursor") {
		t.Fatalf("import card: %v", m.card)
	}
	if f, _ := mcp.ReadFile(mcp.UserPath()); len(f.Servers) != 0 {
		t.Fatal("copied before enter")
	}
	press(m, "down")
	press(m, "space")
	press(m, "enter")
	f, _ := mcp.ReadFile(mcp.UserPath())
	if len(f.Servers) != 1 || f.Servers["two"].Command != "b" {
		t.Fatalf("imported %+v", f.Servers)
	}
}

// /mcp search finds servers in the registry; enter asks for what one
// needs, and a token typed for it goes into mcp.json and nowhere else.
func TestMCPAddFromRegistryAsksForTheToken(t *testing.T) {
	fixture, err := os.ReadFile("../../../core/mcp/testdata/registry.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) }))
	defer srv.Close()
	old := mcp.RegistryURL
	mcp.RegistryURL = srv.URL
	t.Cleanup(func() { mcp.RegistryURL = old })

	m := mcpSession(t, "")
	settle(m, m.slash("/mcp search github"))
	if m.card == nil || !strings.Contains(cardText(m), "smithery-ai-github") {
		t.Fatalf("results: %v", m.card)
	}
	fits(t, m, m.renderCard())
	for i, r := range m.card.rows {
		if r.id == "ai.smithery/smithery-ai-github" {
			m.card.sel = i
		}
	}
	press(m, "enter")
	if m.ask == nil || !strings.Contains(m.input.Placeholder, "Authorization") {
		t.Fatalf("not asking: %+v", m.ask)
	}
	m.input.SetValue("sk-secret-123")
	press(m, "enter")
	if m.ask != nil {
		t.Fatal("still asking")
	}
	f, _ := mcp.ReadFile(mcp.UserPath())
	s := f.Servers["smithery-ai-github"]
	if s.URL != "https://server.smithery.ai/@smithery-ai/github/mcp" || s.Headers["Authorization"] != "Bearer sk-secret-123" {
		t.Fatalf("added %+v", f.Servers)
	}
	for _, it := range m.items {
		if strings.Contains(it.text, "sk-secret") || strings.Contains(rowsText(it), "sk-secret") {
			t.Fatalf("the token is in the transcript: %q", it.text)
		}
	}
	for _, h := range m.history {
		if strings.Contains(h, "sk-secret") {
			t.Fatal("the token is in the history")
		}
	}
	if !strings.Contains(last(m).text, "added MCP server smithery-ai-github") {
		t.Fatalf("last: %q", last(m).text)
	}

	// esc while asking calls it off.
	settle(m, m.slash("/mcp search github"))
	for i, r := range m.card.rows {
		if r.id == "ai.smithery/smithery-ai-github" {
			m.card.sel = i
		}
	}
	press(m, "enter")
	if m.ask == nil {
		t.Fatal("not asking the second time")
	}
	press(m, "esc")
	if m.ask != nil || last(m).text != "cancelled" {
		t.Fatal("esc did not cancel")
	}
}

// /skills lists skills; turning one off takes it out of the system prompt
// and, with none left, the skill tool goes too.
func TestSkillsCardTogglesThePrompt(t *testing.T) {
	m := mcpSession(t, "")
	home := os.Getenv("HOME")
	writeFile(t, filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Work with PDF files.\n---\nUse pypdf.")
	m.system = func() string {
		return prompt.Build(prompt.Options{WorkDir: m.workDir, Model: "m", Skills: skills.Section(skills.Enabled(m.workDir))})
	}
	m.refreshSkills()
	if !strings.Contains(m.agent.System, "- pdf: Work with PDF files.") {
		t.Fatal("skill not in the prompt")
	}
	if _, ok := m.agent.Tools.Get("skill"); !ok {
		t.Fatal("no skill tool")
	}

	m.slash("/skills")
	text := cardText(m)
	if !strings.Contains(text, "pdf") || !strings.Contains(text, "~/.claude/skills") {
		t.Fatalf("card:\n%s", text)
	}
	press(m, "x")
	if !strings.Contains(m.notice, "space turns it off") {
		t.Fatalf("x on a Claude skill: notice %q", m.notice)
	}
	press(m, "space")
	if strings.Contains(m.agent.System, "pdf") {
		t.Fatal("a skill turned off is still in the prompt")
	}
	if _, ok := m.agent.Tools.Get("skill"); ok {
		t.Fatal("the skill tool stayed with no skills on")
	}
	if !strings.Contains(cardText(m), "off") {
		t.Fatal("card does not say off")
	}
}

// MCP calls read as MCP in the transcript and the approval card offers to
// stop asking for the server in this project.
func TestMCPToolLooksLikeATool(t *testing.T) {
	if toolLabel("mcp__github__create_issue") != "MCP" || toolLabel("skill") != "Skill" {
		t.Fatal("labels")
	}
	m := testModel(t, 100, 40)
	m.phase = phaseSession
	m.approval = &agent.ApprovalEvent{Name: "mcp__github__create_issue", Preview: `github · create_issue {"title":"x"}`, Kind: tools.KindExecute, Always: "the github server in this project"}
	card := plain(m.renderApproval())
	if !strings.Contains(card, "github (MCP) wants to run create_issue") || !strings.Contains(card, "don't ask again for the github server in this project") {
		t.Fatalf("approval:\n%s", card)
	}
}

func TestDeferredServerSaysSo(t *testing.T) {
	s := mcp.State{Status: mcp.Ready, Tools: make([]mcp.ToolInfo, 207), Deferred: true}
	if got, _ := statusOf(s); got != "207 tools · loaded on demand" {
		t.Fatalf("status %q", got)
	}
	s.Deferred = false
	if got, _ := statusOf(s); got != "207 tools" {
		t.Fatalf("status %q", got)
	}
}
