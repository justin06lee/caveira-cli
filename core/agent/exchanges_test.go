package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
)

func writeCall(id, path string) *llm.ToolCall {
	c := &llm.ToolCall{ID: id, Type: "function"}
	c.Function.Name = "write_file"
	c.Function.Arguments = `{"path":"` + path + `","content":"x"}`
	return c
}

// sessionAgent is an agent with a session, its files in a scratch home.
func sessionAgent(t *testing.T, s *scripted) *Agent {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.NewSession()
	return ag
}

func TestTurnIsRecordedWithCutNotes(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		writeSSE(sse("", writeCall("c1", "a.txt"))),
		writeSSE(sse("Wrote a.txt. Next I can add tests.", nil)),
	}}
	ag := sessionAgent(t, s)
	collect(ag, context.Background(), "please   write a.txt", nil)
	ag.WaitNotes()

	if ag.Session.Exchanges != 1 {
		t.Fatalf("exchanges %d, want 1", ag.Session.Exchanges)
	}
	xs := loadIndex(ag.Session.Dir())
	if len(xs) != 1 {
		t.Fatalf("index %+v", xs)
	}
	x := xs[0]
	if x.UserNote != "please write a.txt" || !strings.Contains(x.ReplyNote, "Wrote a.txt") || x.Noted {
		t.Errorf("cut notes %+v", x)
	}
	if len(x.Files) != 1 || x.Files[0] != "a.txt" || len(x.Tools) != 1 {
		t.Errorf("files and tools %+v", x)
	}
	rec, err := os.ReadFile(recordPath(ag.Session.Dir(), 1))
	if err != nil || !strings.Contains(string(rec), "## The user wrote") || !strings.Contains(string(rec), "## You called write_file") || !strings.Contains(string(rec), "### Its output") {
		t.Fatalf("record: %v\n%s", err, rec)
	}
	// A free or local session asks the model for nothing more.
	if len(s.bodies) != 2 {
		t.Errorf("%d requests, want 2", len(s.bodies))
	}
}

func TestPaidPlanNotesAreWritten(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		writeSSE(sse("All done, and the build passes.", nil)),
		writeSSE(sse("fix the header", nil)),
		writeSSE(sse("header fixed; build passes", nil)),
	}}
	ag := sessionAgent(t, s)
	ag.paid = func() bool { return true }
	collect(ag, context.Background(), "hey so the header is broken, can you fix it", nil)
	ag.WaitNotes()

	x := loadIndex(ag.Session.Dir())[0]
	if !x.Noted || x.UserNote != "fix the header" || x.ReplyNote != "header fixed; build passes" {
		t.Fatalf("notes %+v", x)
	}
	if !strings.Contains(s.bodies[1], `"model":"qwen-3.5"`) || !strings.Contains(s.bodies[1], "compress") {
		t.Errorf("note request: %s", s.bodies[1])
	}
}

func TestNotesFollowThePlan(t *testing.T) {
	if notesFor(config.Settings{BaseURL: "http://localhost:11434/v1"}) != nil {
		t.Error("a local model got model-written notes")
	}
	if notesFor(config.Settings{KeySource: config.AccountSource}) == nil {
		t.Error("an account session never checks its plan")
	}
}

func TestCompactionBriefsFromRecords(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		writeSSE(sse("", writeCall("c1", "plan.md"))),
		writeSSE(sse("The plan is written.", nil)),
		writeSSE(sse("Second answer.", nil)),
		writeSSE(sse("HANDOFF NOTE", nil)),
		writeSSE(sse("Third answer.", nil)),
	}}
	ag := sessionAgent(t, s)
	ag.ContextWindow, ag.SoftLimit = 1_000_000, 1000
	collect(ag, context.Background(), "write the plan", nil)
	collect(ag, context.Background(), "and a second thing", nil)
	before := append([]llm.Message(nil), ag.Messages...)

	ag.LastPromptTokens = 990
	events := collect(ag, context.Background(), "now the third", nil)
	var ev CompactEvent
	for _, e := range events {
		if c, ok := e.(CompactEvent); ok {
			ev = c
		}
	}
	if !ev.Brief || !ev.Handoff || ev.Dir != ag.Session.Dir() {
		t.Fatalf("compact event %+v", ev)
	}
	if !strings.HasPrefix(ev.Text(), "context compacted · 7 messages, 990 tokens → a brief and a handoff note") {
		t.Errorf("line %q", ev.Text())
	}

	first := ag.Messages[0]
	for _, want := range []string{
		"<compacted-history>",
		"<handoff-note>",
		"HANDOFF NOTE",
		"Files this conversation changed, by the exchanges that changed them",
		"plan.md [1]",
		"<recent>",
		"1. user wrote:",
		"2. user wrote:",
		"full: " + recordPath(ag.Session.Dir(), 1),
		"The user's latest message (exchange 3, which you are in the middle of), as they wrote it:\nnow the third",
	} {
		if !strings.Contains(first.Content, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	// The turn went on from the brief, and finished as exchange 3.
	if last := ag.Messages[len(ag.Messages)-1]; last.Content != "Third answer." || ag.Session.Exchanges != 3 {
		t.Fatalf("turn after compaction: %+v, exchanges %d", last, ag.Session.Exchanges)
	}
	if _, err := os.Stat(ag.Session.Dir() + "/briefs/0001.md"); err != nil {
		t.Errorf("brief not kept: %v", err)
	}

	// The transcript still has everything, with a mark where it compacted.
	h := ag.Session.History()
	var texts []string
	for _, m := range h {
		texts = append(texts, m.Role+":"+m.Content)
	}
	joined := strings.Join(texts, "\n")
	for _, m := range before {
		if m.Content != "" && !strings.Contains(joined, m.Content) {
			t.Errorf("history lost %q", m.Content)
		}
	}
	if !strings.Contains(joined, "user:"+compactedPrefix+".\n") || !strings.Contains(joined, "now the third") || strings.Contains(joined, "<compacted-history>") {
		t.Errorf("history:\n%s", joined)
	}
}

func TestMidTurnCompactionCarriesTheSteps(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		writeSSE(sse("Earlier answer.", nil)),
		writeSSE(sse("", writeCall("c9", "b.txt"))),
		writeSSE(sse("NOTE", nil)),
		writeSSE(sse("Done.", nil)),
	}}
	ag := sessionAgent(t, s)
	ag.ContextWindow, ag.SoftLimit = 1_000_000, 100
	collect(ag, context.Background(), "earlier", nil)
	ag.LastPromptTokens = 0
	collect(ag, context.Background(), "write b.txt", nil)

	// The steps taken before compacting went on after the brief, the
	// call before its output.
	if len(ag.Messages) != 4 || ag.Messages[1].Role != llm.RoleAssistant || len(ag.Messages[1].ToolCalls) != 1 || ag.Messages[2].Role != llm.RoleTool {
		t.Fatalf("history after compaction: %+v", ag.Messages)
	}
	if !strings.Contains(s.bodies[3], `"tool_call_id":"c9"`) || !strings.Contains(s.bodies[3], "which you are in the middle of") {
		t.Errorf("request after compaction: %s", s.bodies[3])
	}
	rec, _ := os.ReadFile(recordPath(ag.Session.Dir(), 2))
	if !strings.Contains(string(rec), "write b.txt") || !strings.Contains(string(rec), "Done.") {
		t.Errorf("record of the compacted turn:\n%s", rec)
	}
}

func TestBriefNamesWhatDoesNotFit(t *testing.T) {
	dir := t.TempDir()
	var xs []Exchange
	for i := 1; i <= 60; i++ {
		xs = append(xs, Exchange{N: i, Prompt: strings.Repeat("p", 2500), Reply: "r", ReplyNote: "note"})
	}
	b := buildBrief(dir, "", xs, "")
	if len(b) > briefBudgetChars {
		t.Errorf("brief %d characters, budget %d", len(b), briefBudgetChars)
	}
	if !strings.Contains(b, "Exchanges 1–") || !strings.Contains(b, "aren't listed here, for room") || !strings.Contains(b, "60. user wrote:") {
		t.Errorf("brief should name the unlisted and keep the latest:\n%.400s", b)
	}
}
