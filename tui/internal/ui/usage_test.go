package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
)

// meSite answers /api/me with whatever body holds now.
type meSite struct {
	*httptest.Server
	mu   sync.Mutex
	body string
}

func (s *meSite) set(body string) {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

// accountSession is a session on a caveira account whose site is a
// meSite answering body.
func accountSession(t *testing.T, w int, body string) (*Model, *meSite) {
	t.Helper()
	m := session(t, w, 40)
	s := &meSite{body: body}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", func(rw http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		rw.Write([]byte(s.body))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	if err := cloud.Save(cloud.Auth{URL: s.URL, Token: "tok", Email: "rebel@example.dev"}); err != nil {
		t.Fatal(err)
	}
	m.cfg.KeySource = config.AccountSource
	return m, s
}

const lightBody = `{"email":"rebel@example.dev","plan":{"id":"lightweight","name":"Lightweight","large":true},"usage":{"used":0.48,"resets_at":"2099-11-20T00:00:00Z"}}`

// panelText is an item drawn width columns wide, without colour.
func panelText(it *item, width int) string { return plain((&renderer{width: width}).render(it)) }

func lastItem(m *Model) *item { return m.items[len(m.items)-1] }

func TestUsageShowsTheAccountAsABar(t *testing.T) {
	m, _ := accountSession(t, 100, lightBody)
	m.totals.Requests, m.totals.InputTokens, m.totals.OutputTokens, m.totals.CostUSD = 2, 3400, 120, 4.5
	cmd := m.slash("/usage")
	if cmd == nil {
		t.Fatal("/usage on an account did not ask the site")
	}
	m.Update(cmd())
	it := lastItem(m)
	if it.kind != itemPanel || it.usage == nil {
		t.Fatalf("/usage pushed %+v", it)
	}
	m.layout()
	dump(t, "usage-account", m.View().Content)
	p := panelText(it, 100)
	for _, want := range []string{"Usage", "Lightweight · 10x the usage of Free", "█", "░", "48% used · resets Nov 20", "This session", "3.4k tokens"} {
		if !strings.Contains(p, want) {
			t.Errorf("usage panel missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "$") {
		t.Errorf("usage panel shows dollars:\n%s", p)
	}
}

func TestUsageBarFitsTheTerminal(t *testing.T) {
	u := &usageInfo{plan: "Lightweight · 10x the usage of Free", used: 0.48, resets: "resets Nov 20"}
	cells := func(width int) int {
		lines := renderUsage(u, width)
		bar := plain(lines[1])
		if w := len([]rune(bar)); w > width {
			t.Errorf("at %d columns the bar line is %d wide", width, w)
		}
		return strings.Count(bar, "█") + strings.Count(bar, "░")
	}
	narrow, wide, huge := cells(48), cells(70), cells(200)
	if !(narrow < wide && wide <= huge) || huge != barMax || narrow < barMin {
		t.Errorf("bar cells at 48, 70, 200 columns: %d, %d, %d", narrow, wide, huge)
	}
	// Too narrow for the words beside it: they go under the bar.
	if lines := renderUsage(u, 30); len(lines) != 3 || !strings.Contains(plain(lines[2]), "48% used") {
		t.Errorf("at 30 columns: %q", lines)
	}
}

func TestUsageForAnAdminIsUnlimited(t *testing.T) {
	m, _ := accountSession(t, 100, `{"email":"a@caveira.app","admin":true,"plan":{"id":"admin","name":"Admin","large":true},"usage":{"used":0.9,"resets_at":"2099-11-20T00:00:00Z"}}`)
	m.Update(m.slash("/usage")())
	p := panelText(lastItem(m), 100)
	if !strings.Contains(p, "Admin · unlimited") {
		t.Errorf("admin usage panel:\n%s", p)
	}
	if strings.ContainsAny(p, "█░") || strings.Contains(p, "% used") {
		t.Errorf("admin usage panel has a bar:\n%s", p)
	}
}

func TestUsageWhenTheSiteCannotBeReached(t *testing.T) {
	m, s := accountSession(t, 100, lightBody)
	s.Close()
	m.Update(m.slash("/usage")())
	p := panelText(lastItem(m), 100)
	if !strings.Contains(p, "couldn't reach caveira.app") || strings.ContainsAny(p, "█░") {
		t.Errorf("usage panel with the site down:\n%s", p)
	}
}

func TestUsageBarTurnsAmberThenRed(t *testing.T) {
	for _, c := range []struct {
		used float64
		want string
	}{
		{0.48, "bone"}, {0.8, "bone"}, {0.81, "warn"}, {0.99, "warn"}, {1, "err"}, {1.2, "err"},
	} {
		got := map[any]string{th.Bone.GetForeground(): "bone", th.Warn.GetForeground(): "warn", th.Err.GetForeground(): "err"}[usageTone(c.used).GetForeground()]
		if got != c.want {
			t.Errorf("at %.2f the bar is %s, want %s", c.used, got, c.want)
		}
	}
	for used, want := range map[float64]string{0: "0%", 0.484: "48%", 0.996: "99%", 1: "100%", 1.3: "100%"} {
		if got := percentUsed(used); got != want {
			t.Errorf("percentUsed(%v) = %s, want %s", used, got, want)
		}
	}
	if b := plain(usageBar(0.999, 10)); strings.Count(b, "█") != 9 {
		t.Errorf("a bar not yet full reads full: %q", b)
	}
}

func TestResetsText(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	if got := resetsText("2026-11-01T00:00:00Z", now); got != "resets Nov 1" {
		t.Errorf("resetsText = %q", got)
	}
	if got := resetsText("2026-10-24T00:00:00Z", now); !strings.HasPrefix(got, "resets at ") {
		t.Errorf("within a day, resetsText = %q", got)
	}
	if got := resetsText("", now); got != "" {
		t.Errorf("no date, resetsText = %q", got)
	}
}

// A reply that takes usage past 80% says so in one line, once, and so
// does the one that uses it all.
func TestRepliesThatCrossAThresholdSaySo(t *testing.T) {
	m, s := accountSession(t, 100, strings.Replace(lightBody, "0.48", "0.7", 1))
	notices := func() []string {
		var out []string
		for _, it := range m.items {
			if it.kind == itemNotice && strings.Contains(it.text, "usage") {
				out = append(out, it.text)
			}
		}
		return out
	}
	m.Update(m.watchUsage()()) // where it stood at the start
	if n := notices(); len(n) != 0 {
		t.Fatalf("the first answer warned: %q", n)
	}
	s.set(strings.Replace(lightBody, "0.48", "0.85", 1))
	m.turnEnded()
	m.Update(m.watchUsage()())
	s.set(strings.Replace(lightBody, "0.48", "0.9", 1))
	m.Update(m.watchUsage()())
	n := notices()
	if len(n) != 1 || n[0] != "85% of this period's usage used · resets Nov 20 · /usage" {
		t.Fatalf("past 80%%: %q", n)
	}
	if !lastItem(m).warn {
		t.Error("the warning is not in amber")
	}
	s.set(strings.Replace(lightBody, "0.48", "1", 1))
	m.Update(m.watchUsage()())
	if n := notices(); len(n) != 2 || n[1] != "this period's usage is all used · resets Nov 20 · /usage" {
		t.Fatalf("at 100%%: %q", n)
	}
}

func TestTurnEndAsksAfterUsageOnlyOnAnAccount(t *testing.T) {
	m := session(t, 100, 40)
	if _, cmd := m.turnEnded(); cmd != nil {
		t.Error("a local model's turn asked the site about usage")
	}
	m, _ = accountSession(t, 100, lightBody)
	if _, cmd := m.turnEnded(); cmd == nil {
		t.Error("an account's turn did not ask about usage")
	}
}

// Nothing in a session reads in dollars: not the footer, not the line
// that closes a turn.
func TestNoDollarsInTheSession(t *testing.T) {
	m := session(t, 140, 40)
	m.totals.Requests, m.totals.InputTokens, m.totals.CostUSD = 4, 12000, 0.42
	m.turnStart = time.Now()
	if s := m.turnSummary(); strings.Contains(s, "$") {
		t.Errorf("turn summary: %q", s)
	}
	info, _ := m.footerInfo(200, keepAll)
	if strings.Contains(plain(info), "$") {
		t.Errorf("footer: %q", plain(info))
	}
}
