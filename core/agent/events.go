package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/justin06lee/caveira/core/llm"
	"github.com/justin06lee/caveira/core/tools"
)

// Event is something the UI wants to know about while a turn runs. Every
// event type is a value; the UI switches on the concrete type.
type Event interface{ isEvent() }

// TextEvent is a fragment of the assistant's visible reply.
type TextEvent struct{ Delta string }

// ReasoningEvent is a fragment of the model's thinking.
type ReasoningEvent struct{ Delta string }

// AssistantDoneEvent marks one complete assistant message. Tool calls, if
// any, follow as ToolStart/ToolEnd pairs before the next assistant message.
type AssistantDoneEvent struct{ Message llm.Message }

// ToolStartEvent fires when a tool call is about to run.
type ToolStartEvent struct {
	ID      string
	Name    string
	Preview string
	Kind    tools.Kind
	Args    json.RawMessage
}

// ToolEndEvent fires when it has finished.
type ToolEndEvent struct {
	ID       string
	Name     string
	Result   tools.Result
	Duration time.Duration
}

// Decision is the answer to an approval request.
type Decision int

const (
	Deny Decision = iota
	Allow
	AllowAlways // allow, and stop asking for this tool this session (or what Always says)
)

// ApprovalEvent asks the UI whether a tool call may run. The agent blocks
// until a Decision is sent on Reply (or the context is cancelled).
type ApprovalEvent struct {
	ID      string
	Name    string
	Preview string
	Kind    tools.Kind
	// Always, when set, is what AllowAlways covers beyond this tool for
	// this session: "the github server in this project".
	Always string
	Reply  chan<- Decision
}

// UsageEvent reports token usage after each model response.
type UsageEvent struct {
	Turn   llm.Usage
	Totals Totals
	// ContextTokens is the prompt size of the last request: how full the
	// context window is right now.
	ContextTokens int
}

// CompactingEvent says a compaction has begun: why (CompactSoftLimit,
// CompactWindow, CompactOverflow or CompactManual), how many tokens the
// last request took, and the limit that set it off.
type CompactingEvent struct {
	Reason string
	Tokens int
	Limit  int
}

// Text is the line the clients show for it.
func (e CompactingEvent) Text() string {
	switch e.Reason {
	case CompactSoftLimit:
		return fmt.Sprintf("near the soft limit (%s / %s) · writing a handoff note…", kTokens(e.Tokens), kTokens(e.Limit))
	case CompactWindow:
		return fmt.Sprintf("near the context window (%s / %s) · writing a handoff note…", kTokens(e.Tokens), kTokens(e.Limit))
	case CompactOverflow:
		return "too long for the model · compacting from the transcript…"
	default:
		return "compacting · writing a handoff note…"
	}
}

// CompactEvent reports that the history was compacted: into the model's
// handoff note when Handoff, else into a brief from the transcript.
type CompactEvent struct {
	BeforeMessages int
	BeforeTokens   int
	Summary        string
	Handoff        bool
	// Brief says the session's record of exchanges made a brief, kept
	// with its records in Dir.
	Brief bool
	Dir   string
}

// Text is the line the clients show for it.
func (e CompactEvent) Text() string {
	into := "a brief from the transcript"
	switch {
	case e.Brief && e.Handoff:
		into = "a brief and a handoff note"
	case e.Brief:
		into = "a brief"
	case e.Handoff:
		into = "a handoff note"
	}
	if e.BeforeTokens > 0 {
		return fmt.Sprintf("context compacted · %d messages, %s tokens → %s", e.BeforeMessages, kTokens(e.BeforeTokens), into)
	}
	return fmt.Sprintf("context compacted · %d messages → %s", e.BeforeMessages, into)
}

// RetryEvent says the model is busy (Reason: llm.RateLimited, Overloaded
// or ConnLost) and the request goes again at Until. Whatever streamed of
// the reply that failed is dropped: it streams again from the start.
type RetryEvent struct {
	Reason string
	Try    int
	Wait   time.Duration
	Until  time.Time
	Err    error
}

// Label is what the clients say the wait is for: the caveira site's own
// words when it is the one asking ("3 requests already running on this
// account, wait a moment"), else the Reason.
func (e RetryEvent) Label() string {
	if s := llm.SiteSays(e.Err); s != "" {
		return s
	}
	return e.Reason
}

// Text is the line the clients show for it, left before the next try.
func (e RetryEvent) Text(left time.Duration) string {
	if left < time.Second {
		return e.Label() + " · retrying now…"
	}
	return e.Label() + " · retrying in " + waitText(left)
}

// ErrorEvent is a failure the turn could not recover from.
type ErrorEvent struct{ Err error }

// DoneEvent ends a turn.
type DoneEvent struct{ Interrupted bool }

func (TextEvent) isEvent()          {}
func (ReasoningEvent) isEvent()     {}
func (AssistantDoneEvent) isEvent() {}
func (ToolStartEvent) isEvent()     {}
func (ToolEndEvent) isEvent()       {}
func (ApprovalEvent) isEvent()      {}
func (UsageEvent) isEvent()         {}
func (CompactingEvent) isEvent()    {}
func (CompactEvent) isEvent()       {}
func (RetryEvent) isEvent()         {}
func (ErrorEvent) isEvent()         {}
func (DoneEvent) isEvent()          {}
