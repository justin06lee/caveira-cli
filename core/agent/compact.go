package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
)

// Compaction: when the history is about to outgrow what a request should
// carry, it is replaced with a handoff note the model writes from all of
// it, or, when no note can be had, with a brief drawn from the transcript
// alone, which needs no model call and so cannot fail.

// Why a compaction runs, as CompactingEvent.Reason gives it.
const (
	CompactManual    = "manual"     // asked for: /compact
	CompactSoftLimit = "soft-limit" // the last request neared the soft token limit
	CompactWindow    = "window"     // the last request neared the model's context window
	CompactOverflow  = "overflow"   // the server refused a request as too long
)

// How near a request may come to the context window, and to the soft
// limit, before the history is compacted.
const (
	windowShare = 0.85
	softShare   = 0.95
)

// compactDue says why the history should be compacted before the next
// request, or "" when it need not be: the last request reached 85% of the
// model's context window or 95% of the soft token limit, whichever is
// lower. limit is the size that share was taken of.
func (a *Agent) compactDue() (reason string, limit int) {
	if a.LastPromptTokens <= 0 || len(a.Messages) < 4 {
		return "", 0
	}
	at := 0.0
	if a.ContextWindow > 0 {
		reason, limit, at = CompactWindow, a.ContextWindow, float64(a.ContextWindow)*windowShare
	}
	if a.SoftLimit > 0 {
		if t := float64(a.SoftLimit) * softShare; reason == "" || t < at {
			reason, limit, at = CompactSoftLimit, a.SoftLimit, t
		}
	}
	if reason == "" || float64(a.LastPromptTokens) < at {
		return "", 0
	}
	return reason, limit
}

const handoffInstruction = `Write a handoff note for a fresh instance of yourself that is about to take over this session with none of the conversation above. Cover, in this order and only from what actually happened:

1. What the user asked for, in their terms, including any preferences or constraints they stated.
2. What has been done: files created or changed (with paths), commands run, and their outcomes.
3. The current state: what works, what is broken, what was verified.
4. What remains to be done, and anything decided about how to do it.
5. Details worth keeping exactly: paths, identifiers, error messages, version numbers, and the user's own words where they matter.

Only include what appears in the conversation above. Never list a file, command, or outcome that is not there; if nothing has been done yet, say so in one line. Be specific and dense. Plain text with short sections; no preamble.`

// handoffNotice is what the model is told when it is to write its
// handoff note: why the session compacts now, then how to write the note.
func handoffNotice(reason string, tokens, limit int) string {
	var why string
	switch reason {
	case CompactSoftLimit:
		why = fmt.Sprintf("This session is at %s tokens of its %s-token soft limit, so it compacts now.", kTokens(tokens), kTokens(limit))
	case CompactWindow:
		why = fmt.Sprintf("This session is at %s tokens of the model's %s-token context window, so it compacts now.", kTokens(tokens), kTokens(limit))
	default:
		why = "The user asked to compact this session now."
	}
	return why + " Don't call any tools and don't carry on with the task: write your handoff note, nothing else.\n\n" + handoffInstruction
}

// Compact compacts the history now, as asked: /compact.
func (a *Agent) Compact(ctx context.Context, emit func(Event)) error {
	return a.compact(ctx, emit, CompactManual, a.ContextWindow)
}

// compact replaces the history with a brief of it: built from the
// session's record of exchanges (brief.go), with the model's handoff note
// in it when one can be had. The note can't be had on an overflow, which
// a request for it would hit again, or when that request fails. An agent
// with no session to keep records in falls back to the note alone, or a
// brief from the live messages. It returns an error only when ctx ends,
// and leaves the history as it was then.
func (a *Agent) compact(ctx context.Context, emit func(Event), reason string, limit int) error {
	if len(a.Messages) == 0 {
		return nil
	}
	before, beforeTokens := len(a.Messages), a.LastPromptTokens
	emit(CompactingEvent{Reason: reason, Tokens: beforeTokens, Limit: limit})

	var note string
	if reason != CompactOverflow {
		n, err := a.handoff(ctx, reason, beforeTokens, limit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			note = n
		}
	}

	ev := CompactEvent{BeforeMessages: before, BeforeTokens: beforeTokens, Handoff: note != ""}
	if a.Session != nil {
		ev.Summary, ev.Dir, ev.Brief = a.briefed(note), a.Session.Dir(), true
	} else {
		ev.Summary = a.replaced(note)
	}
	a.LastPromptTokens = 0
	emit(ev)
	a.save()
	return nil
}

// briefed replaces the history with the brief, and gives the brief. What
// the history held goes to the session's archive, so the transcript keeps
// it. A turn in progress carries on: its prompt goes with the brief as the
// user wrote it, and its steps so far follow as they were, as many of the
// latest as fit in a quarter of the limit.
func (a *Agent) briefed(note string) string {
	a.backfill(a.Messages)
	s := a.Session
	dir := s.Dir()
	indexMu.Lock()
	xs := loadIndex(dir)
	indexMu.Unlock()
	brief := buildBrief(dir, a.WorkDir, xs, note)

	var carried []llm.Message
	dropped := 0
	if len(a.turn) > 1 && len(a.turn)-1 <= len(a.Messages) {
		steps := a.turn[1:]
		room := a.carryRoom()
		size := 0
		from := len(steps)
		for i := len(steps) - 1; i >= 0; i-- {
			size += len(steps[i].Content) + len(steps[i].Reasoning)
			for _, c := range steps[i].ToolCalls {
				size += len(c.Function.Arguments)
			}
			if size > room {
				break
			}
			from = i
		}
		// A tool's output can't come without the call that asked for it.
		for from < len(steps) && steps[from].Role != llm.RoleAssistant {
			from++
		}
		carried, dropped = steps[from:], from
	}
	archive(dir, a.Messages[:len(a.Messages)-len(carried)])
	writeBriefFile(dir, brief)

	text := compactedPrefix + ". Here is what it held.\n\n" + brief
	if len(a.turn) > 0 {
		text += fmt.Sprintf("\n\nThe user's latest message (exchange %d, which you are in the middle of), as they wrote it:\n%s", s.Exchanges+1, cutMiddle(a.turn[0].Content, 8000))
		if dropped > 0 {
			text += fmt.Sprintf("\n\n(The first %d steps you took on it are left out for room; the latest follow.)", dropped)
		}
		a.Messages = append([]llm.Message{{Role: llm.RoleUser, Content: text}}, carried...)
		return brief
	}
	a.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: text + "\n\nContinue from here."},
		{Role: llm.RoleAssistant, Content: "Understood. I have the state from the brief and will continue from there."},
	}
	return brief
}

// carryRoom is how much of a turn in progress, in characters, may go on
// after the brief: a quarter of the lower of the soft limit and the window.
func (a *Agent) carryRoom() int {
	limit := a.SoftLimit
	if a.ContextWindow > 0 && (limit <= 0 || a.ContextWindow < limit) {
		limit = a.ContextWindow
	}
	if limit <= 0 {
		limit = config.DefaultSoftTokenLimit
	}
	return limit / 4 * 4
}

// replaced replaces the history with the handoff note, or with a brief
// from the live messages when there is no note, and gives what replaced it.
func (a *Agent) replaced(note string) string {
	// The note retells the user's request; their latest message goes
	// along as they wrote it, since compaction often comes right after it.
	latest := ""
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if m := a.Messages[i]; m.Role == llm.RoleUser {
			if strings.HasPrefix(m.Content, compactedPrefix) {
				break
			}
			latest = "\n\nThe user's latest message, as they wrote it:\n" + cutMiddle(m.Content, 4000)
			break
		}
	}
	summary, text, ack := note,
		compactedPrefix+". Here is the handoff note from the previous context:\n\n"+note+latest+"\n\nContinue from here.",
		"Understood. I have the state from the handoff note and will continue from there."
	if note == "" {
		summary = a.brief()
		text = compactedPrefix + " without a handoff note, so here is what its transcript records:\n\n" + summary + "\n\nContinue from here."
		ack = "Understood. I have the state from the transcript and will continue from there."
	}
	a.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: text},
		{Role: llm.RoleAssistant, Content: ack},
	}
	return summary
}

// handoff asks the model, with tools off, for its handoff note on the
// whole history.
func (a *Agent) handoff(ctx context.Context, reason string, tokens, limit int) (string, error) {
	msgs := make([]llm.Message, 0, len(a.Messages)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: a.System})
	msgs = append(msgs, a.Messages...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: handoffNotice(reason, tokens, limit)})
	req := llm.Request{Model: a.Model, Messages: msgs, ReasoningEffort: a.Effort}
	if a.Session != nil {
		req.PromptCacheKey = a.Session.ID
	}
	comp, err := a.Client.Stream(ctx, req, nil)
	if err != nil {
		return "", err
	}
	a.account(comp.Usage)
	note := strings.TrimSpace(comp.Message.Content)
	if note == "" {
		return "", fmt.Errorf("the handoff note came back empty")
	}
	return note, nil
}

// briefBudget is about how much of the transcript the brief carries, in
// characters: some 8k tokens.
const briefBudget = 32_000

// brief is what the transcript itself says, for when the model can't write
// a handoff note: the user's latest message, the files the tools changed,
// the last tool calls and reply, and as many earlier messages from the
// user as fit.
func (a *Agent) brief() string {
	var users, files []string
	var calls []llm.ToolCall
	var reply string
	seen := map[string]bool{}
	for _, m := range a.Messages {
		switch m.Role {
		case llm.RoleUser:
			users = append(users, m.Content)
		case llm.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				reply = m.Content
			}
			for _, c := range m.ToolCalls {
				calls = append(calls, c)
				if c.Function.Name != "write_file" && c.Function.Name != "edit_file" {
					continue
				}
				var args struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(c.Function.Arguments), &args) == nil && args.Path != "" && !seen[args.Path] {
					seen[args.Path] = true
					files = append(files, args.Path)
				}
			}
		}
	}

	var tail strings.Builder
	if len(users) > 0 {
		fmt.Fprintf(&tail, "The user's latest message:\n%s\n\n", cutMiddle(users[len(users)-1], 4000))
		users = users[:len(users)-1]
	}
	if len(files) > 0 {
		if len(files) > 40 {
			files = append(files[len(files)-40:], fmt.Sprintf("and %d more", len(files)-40))
		}
		fmt.Fprintf(&tail, "Files your tools changed: %s\n\n", strings.Join(files, ", "))
	}
	if len(calls) > 0 {
		tail.WriteString("Your last tool calls, oldest first:\n")
		for _, c := range calls[max(0, len(calls)-8):] {
			fmt.Fprintf(&tail, "- %s %s\n", c.Function.Name, cutMiddle(strings.Join(strings.Fields(c.Function.Arguments), " "), 200))
		}
		tail.WriteString("\n")
	}
	if reply != "" {
		fmt.Fprintf(&tail, "Your last reply:\n%s\n", cutEnd(reply, 3000))
	}

	// Earlier messages, newest first, as long as there is room.
	room := briefBudget - tail.Len()
	var earlier []string
	for i := len(users) - 1; i >= 0; i-- {
		size := 800
		if strings.HasPrefix(users[i], compactedPrefix) {
			size = 6000
		}
		line := fmt.Sprintf("%d. %s", i+1, cutMiddle(users[i], size))
		if len(line)+1 > room {
			break
		}
		earlier = append([]string{line}, earlier...)
		room -= len(line) + 1
	}
	var b strings.Builder
	if len(earlier) > 0 {
		b.WriteString("The user's earlier messages, oldest first")
		if left := len(users) - len(earlier); left > 0 {
			fmt.Fprintf(&b, " (the first %d left out for room)", left)
		}
		fmt.Fprintf(&b, ":\n%s\n\n", strings.Join(earlier, "\n"))
	}
	b.WriteString(tail.String())
	return strings.TrimSpace(b.String())
}

// cutMiddle shortens text to about max characters by taking out its
// middle, where a long paste matters least.
func cutMiddle(text string, max int) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	head := max * 2 / 3
	return string(r[:head]) + " […] " + string(r[len(r)-(max-head):])
}

// cutEnd keeps the end of text, about max characters of it: a reply's
// close says what it did and what comes next.
func cutEnd(text string, max int) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return "[…] " + string(r[len(r)-max:])
}

// kTokens writes a token count the way the clients show one: 412k, 1M.
func kTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 1000:
		return strconv.Itoa((n+500)/1000) + "k"
	default:
		return strconv.Itoa(n)
	}
}
