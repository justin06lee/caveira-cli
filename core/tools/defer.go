package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/justin06lee/caveira/core/llm"
)

// One MCP server can describe two hundred tools in 800 KB, which would
// fill a model's context before the user says anything. So when a server's
// tools would take too much of it they are deferred: left out of the
// request, named in tool_search's description, and sent from the moment
// the model searches for them, or calls one by its exact name, for the
// rest of the session. Built-in tools are never deferred.

// Grouped is a tool that may be deferred; its group is the MCP server it
// comes from, and a group is deferred or sent as a whole.
type Grouped interface {
	Group() string
}

// DeferShare is how much of the context window deferrable tools may take
// together before the largest groups are deferred.
const DeferShare = 0.10

// DefaultWindow is the context size assumed for a model whose own is not
// known.
const DefaultWindow = 128_000

// SearchName is the tool the model loads deferred tools with.
const SearchName = "tool_search"

// namesBudget is how many bytes of deferred tool names tool_search's
// description may list; a group that does not fit is given as a count.
const namesBudget = 8_000

// resultBudget is how big tool_search's answer may get with the input
// schemas in it; past that it gives names and descriptions only.
const resultBudget = 12_000

func definition(t Tool) llm.Tool {
	schema, _ := json.Marshal(t.Schema())
	return llm.Tool{
		Type:     "function",
		Function: llm.ToolFunction{Name: t.Name(), Description: t.Description(), Parameters: schema},
	}
}

// Tokens estimates what a tool's definition costs in the request, at
// four bytes a token.
func Tokens(t Tool) int {
	b, _ := json.Marshal(definition(t))
	return (len(b) + 3) / 4
}

func groupOf(t Tool) string {
	if g, ok := t.(Grouped); ok {
		return g.Group()
	}
	return ""
}

// Deferred is the groups left out of the request for a model with window
// tokens of context, given what each group's definitions cost. The
// smallest go in while together they fit in DeferShare of the window;
// the rest wait for tool_search.
func Deferred(sizes map[string]int, window int) map[string]bool {
	if window <= 0 {
		window = DefaultWindow
	}
	names := make([]string, 0, len(sizes))
	for g := range sizes {
		names = append(names, g)
	}
	sort.Slice(names, func(i, j int) bool {
		if sizes[names[i]] != sizes[names[j]] {
			return sizes[names[i]] < sizes[names[j]]
		}
		return names[i] < names[j]
	})
	budget := int(float64(window) * DeferShare)
	out := map[string]bool{}
	used := 0
	for _, g := range names {
		if used+sizes[g] <= budget {
			used += sizes[g]
			continue
		}
		out[g] = true
	}
	return out
}

// shortName is an MCP tool's name without mcp__<server>__.
func shortName(name string) string {
	if _, rest, ok := strings.Cut(name, "__"); ok {
		if _, tool, ok := strings.Cut(rest, "__"); ok {
			return tool
		}
	}
	return name
}

// searchTool is tool_search. It is offered only while something is
// deferred.
type searchTool struct{ r *Registry }

func (t *searchTool) Name() string { return SearchName }
func (t *searchTool) Kind() Kind   { return KindRead }

func (t *searchTool) Description() string {
	t.r.mu.RLock()
	defer t.r.mu.RUnlock()
	deferred := Deferred(t.r.sizesLocked(), 0)
	return t.r.searchDescriptionLocked(deferred)
}

const searchIntro = `Load tools that are not in your tool list yet. Some MCP servers offer more tools than fit in your context, so theirs are named below and loaded on demand: search, then call what it finds like any other tool. Loaded tools stay for the rest of the session.

query is keywords ("javascript analyze"; "+github issue" requires "github" in the name) or "select:name1,name2" for exact names. It answers with each tool's full definition.`

func (r *Registry) searchDescriptionLocked(deferred map[string]bool) string {
	byGroup := map[string][]string{}
	for _, name := range r.order {
		if g := groupOf(r.tools[name]); deferred[g] {
			byGroup[g] = append(byGroup[g], shortName(name))
		}
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	// Smallest first, so the budget lists as many groups by name as it can.
	sort.Slice(groups, func(i, j int) bool {
		if len(byGroup[groups[i]]) != len(byGroup[groups[j]]) {
			return len(byGroup[groups[i]]) < len(byGroup[groups[j]])
		}
		return groups[i] < groups[j]
	})
	var b strings.Builder
	b.WriteString(searchIntro)
	b.WriteString("\n\nDeferred tools by MCP server (full name mcp__<server>__<name>):")
	left := namesBudget
	for _, g := range groups {
		list := strings.Join(byGroup[g], ", ")
		if len(list) <= left {
			left -= len(list)
			fmt.Fprintf(&b, "\n%s (%d): %s", g, len(byGroup[g]), list)
		} else {
			fmt.Fprintf(&b, "\n%s: %d tools; search by keyword to find them", g, len(byGroup[g]))
		}
	}
	return b.String()
}

func (t *searchTool) Schema() map[string]any {
	return schema(map[string]any{
		"query":       prop("string", `Keywords, or "select:name1,name2" for exact tool names (full or without mcp__<server>__).`),
		"max_results": prop("integer", "How many tools to load at most (default 5, up to 20)."),
	}, "query")
}

func (t *searchTool) Preview(args json.RawMessage) string {
	var a struct{ Query string }
	_ = json.Unmarshal(args, &a)
	return a.Query
}

func (t *searchTool) Run(_ context.Context, args json.RawMessage) Result {
	var a struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(args, &a); err != nil {
		return errorResult("bad arguments: %v", err)
	}
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return errorResult("query is empty: give keywords or select:name1,name2")
	}
	limit := a.MaxResults
	if limit <= 0 {
		limit = 5
	}
	limit = min(limit, 20)

	found, missing := t.r.Search(q, limit)
	if len(found) == 0 {
		msg := "No tools match " + q + "."
		if len(missing) > 0 {
			msg = "No tool is named " + strings.Join(missing, ", ") + "."
		}
		return Result{Output: msg + " Try other keywords; the deferred tools are named in this tool's description.", Summary: "no matches"}
	}
	t.r.Load(found...)

	t.r.mu.RLock()
	text := func(schemas bool) string {
		var b strings.Builder
		fmt.Fprintf(&b, "Loaded %s; %s in your tool list now, call directly:\n", plural(len(found), "tool"), map[bool]string{true: "it is", false: "they are"}[len(found) == 1])
		for _, name := range found {
			def := map[string]any{"name": name, "description": strings.TrimSpace(t.r.tools[name].Description())}
			if schemas {
				def["parameters"] = t.r.tools[name].Schema()
			}
			d, _ := json.Marshal(def)
			b.WriteString("\n")
			b.Write(d)
		}
		return b.String()
	}
	// The definitions are sent with the request from now on, so a schema
	// is repeated here only while that is cheap; MCP schemas can run to
	// kilobytes each.
	out := text(true)
	if len(out) > resultBudget {
		out = text(false) + "\n\n(Their input schemas are in the tool definitions.)"
	}
	t.r.mu.RUnlock()
	var b strings.Builder
	b.WriteString(out)
	if len(missing) > 0 {
		b.WriteString("\n\nNo tool is named " + strings.Join(missing, ", ") + ".")
	}
	return Result{Output: Bound(b.String(), OutputLimit), Summary: "loaded " + plural(len(found), "tool")}
}

// Search is the deferrable tools matching query, best first, at most
// limit: keywords scored over name and description, or select: with
// exact names. missing is the select: names that matched nothing.
func (r *Registry) Search(query string, limit int) (found, missing []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for _, name := range r.order {
		if groupOf(r.tools[name]) != "" {
			names = append(names, name)
		}
	}

	if list, ok := strings.CutPrefix(strings.TrimSpace(query), "select:"); ok {
		seen := map[string]bool{}
		for _, want := range strings.Split(list, ",") {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			hit := ""
			for _, name := range names {
				if name == want {
					hit = name
					break
				}
				if hit == "" && strings.EqualFold(shortName(name), want) {
					hit = name
				}
			}
			switch {
			case hit == "":
				missing = append(missing, want)
			case !seen[hit]:
				seen[hit] = true
				found = append(found, hit)
			}
		}
		return found, missing
	}

	var terms, required []string
	for _, f := range strings.Fields(strings.ToLower(query)) {
		if req, ok := strings.CutPrefix(f, "+"); ok {
			if req != "" {
				required = append(required, req)
			}
			continue
		}
		terms = append(terms, f)
	}
	terms = append(terms, required...)
	type hit struct {
		name  string
		score int
	}
	var hits []hit
	for _, name := range names {
		if s := score(name, r.tools[name].Description(), terms, required); s > 0 {
			hits = append(hits, hit{name, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	for i := 0; i < len(hits) && i < limit; i++ {
		found = append(found, hits[i].name)
	}
	return found, nil
}

// score weighs a tool against the query's terms: a whole word of its
// name counts most, part of its name less, its description least. A
// required term missing from the name rules it out.
func score(name, desc string, terms, required []string) int {
	lname := strings.ToLower(shortName(name))
	for _, req := range required {
		if !strings.Contains(strings.ToLower(name), req) {
			return 0
		}
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(lname, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9')
	}) {
		words[w] = true
	}
	ldesc := strings.ToLower(desc)
	total := 0
	for _, t := range terms {
		switch {
		case words[t]:
			total += 10
		case strings.Contains(lname, t):
			total += 5
		}
		if n := strings.Count(ldesc, t); n > 0 {
			total += 2 + min(n-1, 3)
		}
	}
	return total
}

// Load sends the named tools from now on even while their group is
// deferred.
func (r *Registry) Load(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded == nil {
		r.loaded = map[string]bool{}
	}
	for _, n := range names {
		r.loaded[n] = true
	}
}

// Loaded is what has been loaded, for the session file.
func (r *Registry) Loaded() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.loaded))
	for n := range r.loaded {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Unload forgets what has been loaded: a new conversation starts with
// deferred tools deferred again.
func (r *Registry) Unload() {
	r.mu.Lock()
	r.loaded = nil
	r.mu.Unlock()
}

// DeferredGroups is the groups whose tools are left out for a model with
// window tokens of context.
func (r *Registry) DeferredGroups(window int) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Deferred(r.sizesLocked(), window)
}

func (r *Registry) sizesLocked() map[string]int {
	sizes := map[string]int{}
	for _, name := range r.order {
		if g := groupOf(r.tools[name]); g != "" {
			sizes[g] += Tokens(r.tools[name])
		}
	}
	return sizes
}
