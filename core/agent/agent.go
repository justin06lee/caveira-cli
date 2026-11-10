// Package agent runs the loop: send the conversation to the model, stream
// the reply, run the tools it asked for, feed the results back, repeat until
// the model answers without tool calls.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
	"github.com/justin06lee/caveira/core/tools"
)

// Totals is cumulative usage for the session.
type Totals struct {
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	CachedTokens int     `json:"cached_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Agent holds one conversation and the means to continue it.
type Agent struct {
	Client  *llm.Client
	Model   string
	Effort  string
	MaxTok  int
	Tools   *tools.Registry
	WorkDir string
	// Confirm makes write and execute tools wait for approval. Set it
	// with SetConfirm once a turn may be running.
	Confirm bool
	// ContextWindow is the model's context size in tokens; compaction kicks
	// in as the conversation approaches it.
	ContextWindow int
	// SoftLimit is the soft token limit: compaction kicks in as a request
	// nears it too, whichever comes first (compact.go).
	SoftLimit int
	// MaxSteps caps model calls per user turn, so a model stuck in a loop
	// cannot spend forever.
	MaxSteps int
	// Patience is how long a turn waits out a busy model before it gives
	// up (retry.go); zero is DefaultPatience.
	Patience Patience
	// Prepare, when set, runs at the start of every turn: MCP servers
	// still starting are waited for there, and their tools put in Tools.
	Prepare func(ctx context.Context)

	System   string
	Messages []llm.Message
	Totals   Totals
	// LastPromptTokens is how many tokens the last request occupied.
	LastPromptTokens int

	Session *Session

	// turn is the messages of the exchange in progress, the prompt first,
	// for its record (exchanges.go); redo says it is the last one taken up
	// again (Continue); paid says whether the small model
	// writes the notes, nil when they are always cuts; notes counts those
	// being written.
	turn  []llm.Message
	redo  bool // turn is the last exchange taken up again (Continue)
	paid  func() bool
	notes sync.WaitGroup

	// mu guards always and Confirm, and Model and ContextWindow against a
	// window that reads them (ModelInfo) while a turn switches them.
	mu     sync.Mutex
	always map[string]bool
}

// New builds an agent for one working directory.
func New(client *llm.Client, cfg config.Settings, workDir, system string) *Agent {
	spec := config.Spec(cfg.Model)
	window := cfg.ContextWindow
	if window <= 0 {
		window = spec.ContextWindow
	}
	soft := cfg.SoftTokenLimit
	if soft <= 0 {
		soft = config.DefaultSoftTokenLimit
	}
	return &Agent{
		Client:        client,
		Model:         cfg.Model,
		Effort:        cfg.ReasoningEffort,
		MaxTok:        cfg.MaxTokens,
		Tools:         tools.Default(workDir),
		WorkDir:       workDir,
		Confirm:       cfg.Confirm,
		ContextWindow: window,
		SoftLimit:     soft,
		MaxSteps:      200,
		Patience:      DefaultPatience,
		paid:          notesFor(cfg),
		System:        system,
		always:        map[string]bool{},
	}
}

// SetModel switches models mid-session and refreshes the context window.
func (a *Agent) SetModel(model string, window int) {
	if window <= 0 {
		window = config.Spec(model).ContextWindow
	}
	a.mu.Lock()
	a.Model, a.ContextWindow = model, window
	a.mu.Unlock()
}

// SetConfirm turns asking before writes and commands on or off, and is
// safe to call while a turn runs: the next tool call goes by it.
func (a *Agent) SetConfirm(on bool) {
	a.mu.Lock()
	a.Confirm = on
	a.mu.Unlock()
}

// Confirming says whether writes and commands wait for approval.
func (a *Agent) Confirming() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Confirm
}

// asks says whether a call to the tool name waits for approval.
func (a *Agent) asks(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Confirm && !a.always[name]
}

// ModelInfo is the model and context window, safe to call while a turn
// runs in another goroutine.
func (a *Agent) ModelInfo() (model string, window int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Model, a.ContextWindow
}

// Reset forgets the conversation but keeps the configuration.
func (a *Agent) Reset() {
	a.Messages = nil
	a.LastPromptTokens = 0
	a.Session = nil
	if a.Tools != nil {
		a.Tools.Unload()
	}
}

func (a *Agent) request(withTools bool) llm.Request {
	msgs := make([]llm.Message, 0, len(a.Messages)+1)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: a.System})
	msgs = append(msgs, a.Messages...)
	req := llm.Request{
		Model:           a.Model,
		Messages:        msgs,
		MaxTokens:       a.MaxTok,
		ReasoningEffort: a.Effort,
	}
	if a.Session != nil {
		req.PromptCacheKey = a.Session.ID
	}
	if withTools {
		_, window := a.ModelInfo()
		req.Tools = a.Tools.Definitions(window)
	}
	return req
}

// Run performs one user turn, emitting events until DoneEvent.
func (a *Agent) Run(ctx context.Context, input string, emit func(Event)) {
	a.backfill(a.Messages)
	a.turn, a.redo = nil, false
	a.push(llm.Message{Role: llm.RoleUser, Content: input})
	defer a.save()
	defer a.endExchange()
	a.loop(ctx, emit)
}

// loop runs the turn in a.turn to its end: model, tools, model again.
func (a *Agent) loop(ctx context.Context, emit func(Event)) {
	if a.Prepare != nil {
		a.Prepare(ctx)
	}

	steps := a.MaxSteps
	if steps <= 0 {
		steps = 200
	}
	retriedContext := false

	for step := 0; step < steps; step++ {
		if reason, limit := a.compactDue(); reason != "" {
			if err := a.compact(ctx, emit, reason, limit); err != nil && ctx.Err() != nil {
				emit(DoneEvent{Interrupted: true})
				return
			}
		}

		comp, err := a.stream(ctx, a.request(true), emit)

		if ctx.Err() != nil {
			// Interrupted. Keep whatever text arrived so the transcript and
			// the history agree, but drop half-parsed tool calls.
			if comp != nil && strings.TrimSpace(comp.Message.Content) != "" {
				a.push(llm.Message{Role: llm.RoleAssistant, Content: comp.Message.Content + "\n\n[interrupted by user]"})
			}
			emit(DoneEvent{Interrupted: true})
			return
		}
		if err != nil {
			if isContextOverflow(err) && !retriedContext && len(a.Messages) > 2 {
				retriedContext = true
				if cerr := a.compact(ctx, emit, CompactOverflow, a.ContextWindow); cerr == nil {
					continue
				}
			}
			emit(ErrorEvent{Err: err})
			emit(DoneEvent{})
			return
		}

		a.account(comp.Usage)
		emit(UsageEvent{Turn: comp.Usage, Totals: a.Totals, ContextTokens: a.LastPromptTokens})

		msg := comp.Message
		if len(msg.ToolCalls) == 0 {
			// A reply that is only tool calls written as text runs as
			// those calls; the text itself is dropped, which also takes it
			// off the screen when AssistantDone replaces what streamed.
			if calls := textToolCalls(msg.Content, a.hasTool); calls != nil {
				msg.ToolCalls, msg.Content = calls, ""
			}
		}
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			if comp.FinishReason == "length" {
				emit(ErrorEvent{Err: errors.New("the model hit its output limit before saying anything")})
			}
			// An empty reply ends the turn; nothing to feed back.
			a.push(msg)
			emit(AssistantDoneEvent{Message: msg})
			emit(DoneEvent{})
			return
		}
		a.push(msg)
		emit(AssistantDoneEvent{Message: msg})

		if len(msg.ToolCalls) == 0 {
			emit(DoneEvent{})
			return
		}

		for _, call := range msg.ToolCalls {
			a.push(a.runTool(ctx, call, emit))
		}
		if ctx.Err() != nil {
			emit(DoneEvent{Interrupted: true})
			return
		}
	}
	emit(ErrorEvent{Err: fmt.Errorf("stopped after %d model calls in one turn; send another message to continue", steps)})
	emit(DoneEvent{})
}

// runTool executes one call and returns the tool message for the history.
// Every call gets a result, even a cancelled one, because a tool_call without
// a matching tool message makes the whole history invalid.
func (a *Agent) runTool(ctx context.Context, call llm.ToolCall, emit func(Event)) llm.Message {
	name := call.Function.Name
	args := json.RawMessage(call.Function.Arguments)
	reply := func(r tools.Result) llm.Message {
		content := r.Output
		if strings.TrimSpace(content) == "" {
			content = "(no output)"
		}
		return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Name: name, Content: content}
	}

	if ctx.Err() != nil {
		return reply(tools.Result{Output: "Cancelled by the user before this ran.", Summary: "cancelled", IsError: true})
	}

	tool, known := a.Tools.Get(name)
	preview := ""
	kind := tools.KindRead
	if known {
		preview = tool.Preview(args)
		kind = tool.Kind()
	}
	emit(ToolStartEvent{ID: call.ID, Name: name, Preview: preview, Kind: kind, Args: args})
	start := time.Now()

	remembered, _ := tool.(tools.Remembered)
	if known && kind != tools.KindRead && a.asks(name) && (remembered == nil || !remembered.AlwaysAllowed()) {
		ch := make(chan Decision, 1)
		ev := ApprovalEvent{ID: call.ID, Name: name, Preview: preview, Kind: kind, Reply: ch}
		if remembered != nil {
			ev.Always = remembered.AlwaysScope()
		}
		emit(ev)
		var d Decision
		select {
		case d = <-ch:
		case <-ctx.Done():
			r := tools.Result{Output: "Cancelled by the user.", Summary: "cancelled", IsError: true}
			emit(ToolEndEvent{ID: call.ID, Name: name, Result: r, Duration: time.Since(start)})
			return reply(r)
		}
		switch d {
		case AllowAlways:
			// An MCP server is allowed for the project, on disk; if that
			// cannot be written it is allowed for this session.
			if remembered == nil || remembered.AllowAlways() != nil {
				a.mu.Lock()
				a.always[name] = true
				a.mu.Unlock()
			}
		case Deny:
			r := tools.Result{
				Output:  "The user declined this tool call. Do not retry it as-is; ask what they would prefer or take another approach.",
				Summary: "denied", IsError: true,
			}
			emit(ToolEndEvent{ID: call.ID, Name: name, Result: r, Duration: time.Since(start)})
			return reply(r)
		}
	}

	result := a.Tools.Run(ctx, name, args)
	emit(ToolEndEvent{ID: call.ID, Name: name, Result: result, Duration: time.Since(start)})
	return reply(result)
}

func (a *Agent) hasTool(name string) bool {
	_, ok := a.Tools.Get(name)
	return ok
}

func (a *Agent) account(u llm.Usage) {
	a.Totals.Requests++
	a.Totals.InputTokens += u.PromptTokens
	a.Totals.CachedTokens += u.Cached()
	a.Totals.OutputTokens += u.CompletionTokens
	spec := config.Spec(a.Model)
	fresh := u.PromptTokens - u.Cached()
	if fresh < 0 {
		fresh = 0
	}
	a.Totals.CostUSD += float64(fresh)*spec.InputPerM/1e6 +
		float64(u.Cached())*spec.CachedPerM/1e6 +
		float64(u.CompletionTokens)*spec.OutputPerM/1e6
	if u.PromptTokens > 0 {
		a.LastPromptTokens = u.PromptTokens + u.CompletionTokens
	}
}

func isContextOverflow(err error) bool {
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		return false
	}
	m := strings.ToLower(apiErr.Message + " " + apiErr.Code)
	return strings.Contains(m, "context") && (strings.Contains(m, "length") || strings.Contains(m, "window") || strings.Contains(m, "exceed") || strings.Contains(m, "too long") || strings.Contains(m, "maximum"))
}

// push adds messages of the turn in progress to the history and to its
// record.
func (a *Agent) push(msgs ...llm.Message) {
	a.Messages = append(a.Messages, msgs...)
	a.turn = append(a.turn, msgs...)
}

// Note appends a user-side note to the history without running a turn: the
// output of a shell command the user ran themselves, for instance.
func (a *Agent) Note(text string) {
	a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: text})
	a.save()
}
