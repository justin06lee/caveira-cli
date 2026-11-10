// Package tools is what the agent can do to the machine: read, search, edit,
// write, and run. Each tool describes itself to the model and executes one
// call. Outputs are bounded so a careless `cat` cannot flood the context.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/justin06lee/caveira/core/llm"
)

// Kind is how much a tool can change: it decides what needs confirmation.
type Kind int

const (
	KindRead    Kind = iota // looks at files; never needs confirmation
	KindWrite               // changes files under the working directory
	KindExecute             // runs arbitrary commands
)

func (k Kind) String() string {
	switch k {
	case KindWrite:
		return "write"
	case KindExecute:
		return "execute"
	default:
		return "read"
	}
}

// Result is what a tool call produced.
type Result struct {
	// Output is the text the model sees.
	Output string
	// Summary is one short line for the UI: "42 lines", "exit 0 · 3 lines".
	Summary string
	// Diff, when set, is a unified-ish diff the UI shows for edits.
	Diff string
	// Path is the file touched, if any, relative to the working directory.
	Path    string
	IsError bool
}

func errorResult(format string, args ...any) Result {
	msg := fmt.Sprintf(format, args...)
	return Result{Output: "Error: " + msg, Summary: msg, IsError: true}
}

// Tool is one capability.
type Tool interface {
	Name() string
	Description() string
	// Schema is the JSON schema of the arguments object.
	Schema() map[string]any
	Kind() Kind
	// Preview is the one-liner shown while the call is running, from the
	// parsed arguments: the path, the command, the pattern.
	Preview(args json.RawMessage) string
	Run(ctx context.Context, args json.RawMessage) Result
}

// Remembered is a tool whose "always allow" outlives the session and
// covers more than itself: an MCP server's tools, allowed for a project.
type Remembered interface {
	// AlwaysAllowed says calls run without asking.
	AlwaysAllowed() bool
	// AllowAlways stops asking from now on.
	AllowAlways() error
	// AlwaysScope says what AllowAlways covers, in words that follow
	// "don't ask again for": "the github server in this project".
	AlwaysScope() string
}

// Registry is the tool set for one working directory. Tools can come and
// go while a session runs (MCP servers start and stop), so it is safe to
// use from more than one goroutine.
type Registry struct {
	WorkDir string
	mu      sync.RWMutex
	tools   map[string]Tool
	order   []string
	// loaded is the deferrable tools sent even while their group is
	// deferred, by name, so they outlast their server restarting.
	loaded map[string]bool
}

// Default is the standard coding tool set rooted at dir.
func Default(dir string) *Registry {
	r := &Registry{WorkDir: dir, tools: map[string]Tool{}}
	for _, t := range []Tool{
		&readTool{dir}, &writeTool{dir}, &editTool{dir},
		&bashTool{dir}, &globTool{dir}, &grepTool{dir}, &listTool{dir},
		&searchTool{r},
	} {
		r.Register(t)
	}
	return r
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name()]; !dup {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Unregister removes every tool whose name starts with prefix.
func (r *Registry) Unregister(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.order[:0]
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Definitions is the tool list in the wire shape the model expects, for
// a model with window tokens of context (0 when not known). Deferred
// groups are left out but for the tools loaded from them, and
// tool_search is offered only while something is deferred.
func (r *Registry) Definitions(window int) []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	deferred := Deferred(r.sizesLocked(), window)
	out := make([]llm.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		if g := groupOf(t); deferred[g] && !r.loaded[name] {
			continue
		}
		if name == SearchName {
			if _, ok := t.(*searchTool); ok {
				if len(deferred) == 0 {
					continue
				}
				d := definition(t)
				d.Function.Description = r.searchDescriptionLocked(deferred)
				out = append(out, d)
				continue
			}
		}
		out = append(out, definition(t))
	}
	return out
}

// Run executes one call. Unknown tools and unparseable arguments come back as
// error results rather than Go errors, so the model gets to read them and
// correct itself.
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) Result {
	r.mu.RLock()
	t, ok := r.tools[name]
	var names []string
	grouped := false
	if !ok {
		for _, n := range r.order {
			if groupOf(r.tools[n]) != "" {
				grouped = true
				continue
			}
			names = append(names, n)
		}
	}
	r.mu.RUnlock()
	if !ok {
		sort.Strings(names)
		more := ""
		if grouped {
			more = "; MCP tools are found with " + SearchName
		}
		return errorResult("unknown tool %q. Available tools: %s%s", name, strings.Join(names, ", "), more)
	}
	if groupOf(t) != "" {
		// A deferred tool called by its exact name is as good as found.
		r.Load(name)
	}
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return errorResult("arguments for %s are not valid JSON: %s", name, clip(string(args), 200))
	}
	return t.Run(ctx, args)
}

// resolve turns a model-supplied path into an absolute one under dir, and a
// display form relative to dir, with forward slashes (which Windows takes
// as well as backslashes, and the models write better). Absolute paths are
// honored: the agent is allowed to work outside the project when asked to.
// That is the rule on every system, so there is no path check to get
// around: what stops a write or a command is the approval, which is asked
// for per tool, never per path.
//
// On Windows a path can also be rooted without a drive (\x), or carry a
// drive and no root (C:x, relative to that drive's own working directory,
// which caveira has no way to know). The first is taken to be on dir's
// drive; the second under dir when it names dir's drive and at that
// drive's root when it names another, rather than as a file called "C:x"
// in dir, which NTFS would read as a data stream.
func resolve(dir, p string) (abs, rel string) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return dir, "."
	}
	if p == "~" || strings.HasPrefix(p, "~/") || (strings.HasPrefix(p, `~\`) && filepath.Separator == '\\') {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	switch vol := filepath.VolumeName(p); {
	case filepath.IsAbs(p):
	case vol != "":
		rest := p[len(vol):]
		if strings.EqualFold(vol, filepath.VolumeName(dir)) {
			p = filepath.Join(dir, rest)
		} else {
			p = vol + string(filepath.Separator) + rest
		}
	case os.IsPathSeparator(p[0]):
		p = filepath.VolumeName(dir) + p
	default:
		p = filepath.Join(dir, p)
	}
	abs = filepath.Clean(p)
	if r, err := filepath.Rel(dir, abs); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		rel = filepath.ToSlash(r)
	} else {
		rel = filepath.ToSlash(abs)
	}
	return abs, rel
}

// decode reads a call's arguments. Small models often quote a boolean or
// a number ("replace_all": "false"); a field that does not fit because of
// that is read as what its string says.
func decode(args json.RawMessage, into any) error {
	err := json.NewDecoder(strings.NewReader(string(args))).Decode(into)
	var fields map[string]any
	for tries := 0; err != nil && tries < 8; tries++ {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) || typeErr.Value != "string" {
			return err
		}
		if fields == nil && json.Unmarshal(args, &fields) != nil {
			return err
		}
		s, ok := fields[typeErr.Field].(string)
		if !ok {
			return err
		}
		switch typeErr.Type.Kind() {
		case reflect.Bool:
			b, perr := strconv.ParseBool(strings.TrimSpace(s))
			if perr != nil {
				return err
			}
			fields[typeErr.Field] = b
		case reflect.Int, reflect.Int64, reflect.Int32, reflect.Float64:
			n, perr := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if perr != nil {
				return err
			}
			fields[typeErr.Field] = n
		default:
			return err
		}
		fixed, _ := json.Marshal(fields)
		err = json.Unmarshal(fixed, into)
	}
	return err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Bound cuts s to about limit bytes the way command output is cut, head
// and tail kept with a marker between, for tools outside this package.
func Bound(s string, limit int) string {
	out, _ := truncateOutput(s, limit)
	return out
}

// OutputLimit is how much of a command's output the model sees.
const OutputLimit = bashOutputLimit

// truncateOutput keeps the head and tail of a long output with a marker in
// between: both ends matter for command output (the command echo and the
// final error), and the middle is usually the least informative part.
func truncateOutput(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	head := limit * 2 / 3
	tail := limit - head
	omitted := len(s) - head - tail
	// Cut on line boundaries where possible so the marker sits on its own line.
	if i := strings.LastIndexByte(s[:head], '\n'); i > head/2 {
		head = i
	}
	if i := strings.IndexByte(s[len(s)-tail:], '\n'); i >= 0 && i < tail/2 {
		tail -= i + 1
	}
	return s[:head] + fmt.Sprintf("\n\n… [%d bytes truncated] …\n\n", omitted) + s[len(s)-tail:], true
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	if len(b) == 0 {
		return false
	}
	if !utf8.Valid(b) {
		// Could be a cut multibyte sequence at the end; check for NULs too.
		nul := 0
		for _, c := range b {
			if c == 0 {
				nul++
			}
		}
		return nul > 0 || !utf8.Valid(b[:len(b)-4])
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

// skipDir is the set of directories no search should descend into unless
// asked for by name. They are huge and never what the model wants.
var skipDir = map[string]bool{
	".git": true, "node_modules": true, ".next": true, "dist": true, "build": true,
	"target": true, ".venv": true, "venv": true, "__pycache__": true, ".cache": true,
	".turbo": true, "vendor": true, ".idea": true, ".vscode": true, ".DS_Store": true,
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
