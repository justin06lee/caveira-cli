package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
)

func TestCompactDue(t *testing.T) {
	four := make([]llm.Message, 4)
	cases := []struct {
		name               string
		window, soft, used int
		msgs               []llm.Message
		reason             string
		limit              int
	}{
		// Qwen3.5: 85% of 262,144 is 222,822.4, well under 95% of 434k.
		{"qwen under the window share", 262_144, 434_000, 222_822, four, "", 0},
		{"qwen at the window share", 262_144, 434_000, 222_823, four, CompactWindow, 262_144},
		// GLM: 95% of 434k is 412,300, well under 85% of 1M.
		{"glm under the soft share", 1_000_000, 434_000, 412_299, four, "", 0},
		{"glm at the soft share", 1_000_000, 434_000, 412_300, four, CompactSoftLimit, 434_000},
		{"glm past the soft share", 1_000_000, 434_000, 600_000, four, CompactSoftLimit, 434_000},
		{"no window known", 0, 100_000, 95_000, four, CompactSoftLimit, 100_000},
		{"no soft limit", 100_000, 0, 85_000, four, CompactWindow, 100_000},
		{"neither", 0, 0, 900_000, four, "", 0},
		{"nothing sent yet", 1_000_000, 434_000, 0, four, "", 0},
		{"too short to compact", 1_000_000, 434_000, 500_000, make([]llm.Message, 3), "", 0},
	}
	for _, c := range cases {
		a := &Agent{ContextWindow: c.window, SoftLimit: c.soft, LastPromptTokens: c.used, Messages: c.msgs}
		reason, limit := a.compactDue()
		if reason != c.reason || limit != c.limit {
			t.Errorf("%s: got %q %d, want %q %d", c.name, reason, limit, c.reason, c.limit)
		}
	}
}

func TestSoftLimitFromSettings(t *testing.T) {
	if a := New(llm.New("http://x", ""), config.Settings{Model: "test-model"}, t.TempDir(), ""); a.SoftLimit != config.DefaultSoftTokenLimit {
		t.Errorf("default soft limit %d, want %d", a.SoftLimit, config.DefaultSoftTokenLimit)
	}
	if a := New(llm.New("http://x", ""), config.Settings{Model: "test-model", SoftTokenLimit: 200_000}, t.TempDir(), ""); a.SoftLimit != 200_000 {
		t.Errorf("soft limit %d, want 200000", a.SoftLimit)
	}
}

// history is four messages of an earlier exchange in which a file was
// written.
func history() []llm.Message {
	call := llm.ToolCall{ID: "c1", Type: "function"}
	call.Function.Name = "write_file"
	call.Function.Arguments = `{"path":"notes/plan.md","content":"x"}`
	return []llm.Message{
		{Role: llm.RoleUser, Content: "write the plan"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "wrote notes/plan.md"},
		{Role: llm.RoleAssistant, Content: "The plan is in notes/plan.md."},
	}
}

func TestSoftLimitWritesHandoffNote(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		writeSSE(sse("HANDOFF NOTE", nil)),
		writeSSE(sse("Continuing.", nil)),
	}}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.ContextWindow, ag.SoftLimit = 1_000_000, 1000
	ag.Messages = history()
	ag.LastPromptTokens = 960
	events := collect(ag, context.Background(), "now the tests", nil)

	var order []string
	for _, ev := range events {
		switch ev := ev.(type) {
		case CompactingEvent:
			if ev.Reason != CompactSoftLimit || ev.Tokens != 960 || ev.Limit != 1000 {
				t.Errorf("compacting event %+v", ev)
			}
			if !strings.HasPrefix(ev.Text(), "near the soft limit (960 / 1k)") {
				t.Errorf("compacting line %q", ev.Text())
			}
			order = append(order, "compacting")
		case CompactEvent:
			if !ev.Handoff || ev.Summary != "HANDOFF NOTE" {
				t.Errorf("compact event %+v", ev)
			}
			order = append(order, "compacted")
		}
	}
	if strings.Join(order, ",") != "compacting,compacted" {
		t.Fatalf("events %v", order)
	}
	// The model was told why, asked for the note, and offered no tools.
	if body := s.bodies[0]; strings.Contains(body, `"tools"`) || !strings.Contains(body, "1k-token soft limit") || !strings.Contains(body, "handoff note") {
		t.Fatalf("handoff request: %s", body)
	}
	first := ag.Messages[0].Content
	if !strings.Contains(first, "HANDOFF NOTE") || !strings.Contains(first, "The user's latest message, as they wrote it:\nnow the tests") {
		t.Fatalf("history after compaction: %q", first)
	}
	if last := ag.Messages[len(ag.Messages)-1]; last.Content != "Continuing." {
		t.Fatalf("turn did not continue: %+v", last)
	}
}

func TestFailedHandoffFallsBackToBrief(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		func(w http.ResponseWriter) {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"something else went wrong"}}`)
		},
		writeSSE(sse("Continuing.", nil)),
	}}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.ContextWindow, ag.SoftLimit = 1000, 0
	ag.Messages = history()
	ag.LastPromptTokens = 900
	events := collect(ag, context.Background(), "now the tests", nil)

	var compacted *CompactEvent
	for _, ev := range events {
		if c, ok := ev.(CompactEvent); ok {
			compacted = &c
		}
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("the failed note ended the turn: %v", e.Err)
		}
	}
	if compacted == nil || compacted.Handoff {
		t.Fatalf("no compaction from the transcript: %+v", compacted)
	}
	if !strings.HasPrefix(compacted.Text(), "context compacted · 5 messages, 900 tokens → a brief from the transcript") {
		t.Errorf("compacted line %q", compacted.Text())
	}
	brief := ag.Messages[0].Content
	for _, want := range []string{
		"without a handoff note",
		"The user's latest message:\nnow the tests",
		"1. write the plan",
		"Files your tools changed: notes/plan.md",
		"- write_file",
		"Your last reply:\nThe plan is in notes/plan.md.",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	if last := ag.Messages[len(ag.Messages)-1]; last.Content != "Continuing." {
		t.Fatalf("turn did not continue: %+v", last)
	}
}

func TestBriefStaysInBudget(t *testing.T) {
	ag := &Agent{}
	for i := 0; i < 200; i++ {
		ag.Messages = append(ag.Messages,
			llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("request %d %s", i, strings.Repeat("x", 2000))},
			llm.Message{Role: llm.RoleAssistant, Content: "done"})
	}
	b := ag.brief()
	if len(b) > briefBudget+200 {
		t.Errorf("brief is %d characters, budget %d", len(b), briefBudget)
	}
	if !strings.Contains(b, "left out for room") || !strings.Contains(b, "request 199") {
		t.Errorf("brief should keep the latest and say what it left out:\n%.300s", b)
	}
}

func TestKTokens(t *testing.T) {
	for n, want := range map[int]string{960: "960", 1000: "1k", 262_144: "262k", 412_300: "412k", 434_000: "434k", 1_000_000: "1M", 1_200_000: "1.2M"} {
		if got := kTokens(n); got != want {
			t.Errorf("kTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
