package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeMCP is a tool from an MCP server, as the registry sees one.
type fakeMCP struct {
	server, tool, desc string
	pad                int // bytes of schema, to make a server big
}

func (t *fakeMCP) Name() string                   { return "mcp__" + t.server + "__" + t.tool }
func (t *fakeMCP) Group() string                  { return t.server }
func (t *fakeMCP) Description() string            { return t.desc }
func (t *fakeMCP) Kind() Kind                     { return KindExecute }
func (t *fakeMCP) Preview(json.RawMessage) string { return "" }
func (t *fakeMCP) Run(context.Context, json.RawMessage) Result {
	return Result{Output: "ran " + t.tool}
}
func (t *fakeMCP) Schema() map[string]any {
	return schema(map[string]any{"x": prop("string", strings.Repeat("p", t.pad))})
}

func defNames(r *Registry, window int) map[string]bool {
	out := map[string]bool{}
	for _, d := range r.Definitions(window) {
		out[d.Function.Name] = true
	}
	return out
}

// big is a server whose tools take about 200k tokens, like REA.
func big(r *Registry) {
	r.Register(&fakeMCP{server: "rea", tool: "analyze_javascript_application", desc: "Analyze a JavaScript application for vulnerabilities.", pad: 4000})
	r.Register(&fakeMCP{server: "rea", tool: "decompile_binary", desc: "Decompile a native binary; works on javascript bundles too.", pad: 4000})
	r.Register(&fakeMCP{server: "rea", tool: "scan_ports", desc: "Scan the open ports of a host.", pad: 4000})
	for i := 0; i < 200; i++ {
		r.Register(&fakeMCP{server: "rea", tool: fmt.Sprintf("tool_%03d", i), desc: "Something else entirely.", pad: 4000})
	}
}

func TestSmallServersAreSentAsToday(t *testing.T) {
	r := Default(t.TempDir())
	r.Register(&fakeMCP{server: "github", tool: "create_issue", desc: "Open an issue."})
	got := defNames(r, 128_000)
	if !got["mcp__github__create_issue"] || got[SearchName] {
		t.Fatalf("small server should be sent whole, without tool_search: %v", got)
	}
	if len(r.DeferredGroups(128_000)) != 0 {
		t.Fatal("nothing should be deferred")
	}
}

func TestThresholdDefersBigServerOnly(t *testing.T) {
	r := Default(t.TempDir())
	r.Register(&fakeMCP{server: "github", tool: "create_issue", desc: "Open an issue."})
	big(r)
	got := defNames(r, 128_000)
	if got["mcp__rea__scan_ports"] || !got[SearchName] {
		t.Fatalf("rea should be deferred behind tool_search: %v", got)
	}
	if !got["mcp__github__create_issue"] || !got["bash"] || !got["read_file"] {
		t.Fatalf("built-ins and small servers stay: %v", got)
	}
	// The same tools fit a model with a ten-million-token window.
	if got := defNames(r, 10_000_000); !got["mcp__rea__scan_ports"] || got[SearchName] {
		t.Fatal("a big enough window sends everything")
	}
	// Unknown window: 128k is assumed.
	if !defNames(r, 0)[SearchName] {
		t.Fatal("unknown window should defer")
	}
	var desc string
	for _, d := range r.Definitions(128_000) {
		if d.Function.Name == SearchName {
			desc = d.Function.Description
		}
	}
	if !strings.Contains(desc, "rea (203): ") || !strings.Contains(desc, "analyze_javascript_application") || strings.Contains(desc, "create_issue") {
		t.Fatalf("tool_search should name rea's tools only: %s", desc)
	}
}

func TestDeferredGroupsSmallestFirst(t *testing.T) {
	d := Deferred(map[string]int{"a": 5000, "b": 6000, "c": 100}, 100_000) // budget 10k
	if d["a"] || d["c"] || !d["b"] {
		t.Fatalf("got %v", d)
	}
}

func TestSearchRanking(t *testing.T) {
	r := Default(t.TempDir())
	big(r)
	found, _ := r.Search("javascript", 5)
	if len(found) != 2 || found[0] != "mcp__rea__analyze_javascript_application" || found[1] != "mcp__rea__decompile_binary" {
		t.Fatalf("name match should beat description match: %v", found)
	}
	if found, _ := r.Search("+scan host", 5); len(found) != 1 || found[0] != "mcp__rea__scan_ports" {
		t.Fatalf("required term: %v", found)
	}
	if found, _ := r.Search("tool", 3); len(found) != 3 {
		t.Fatalf("limit: %v", found)
	}
	if found, _ := r.Search("bash", 5); len(found) != 0 {
		t.Fatalf("built-ins are not searched: %v", found)
	}
}

func TestSelect(t *testing.T) {
	r := Default(t.TempDir())
	big(r)
	found, missing := r.Search("select:scan_ports, mcp__rea__decompile_binary,nope,scan_ports", 5)
	if strings.Join(found, ",") != "mcp__rea__scan_ports,mcp__rea__decompile_binary" || strings.Join(missing, ",") != "nope" {
		t.Fatalf("found %v missing %v", found, missing)
	}
}

func TestSearchLoadsForTheSession(t *testing.T) {
	r := Default(t.TempDir())
	big(r)
	res := r.Run(context.Background(), SearchName, json.RawMessage(`{"query":"javascript","max_results":1}`))
	if res.IsError || !strings.Contains(res.Output, `"name":"mcp__rea__analyze_javascript_application"`) || !strings.Contains(res.Output, `"parameters":{`) {
		t.Fatalf("search result: %+v", res)
	}
	got := defNames(r, 128_000)
	if !got["mcp__rea__analyze_javascript_application"] || got["mcp__rea__decompile_binary"] {
		t.Fatalf("only the found tool is loaded: %v", got)
	}
	// The server restarting (tools unregistered, registered again) keeps it.
	r.Unregister("mcp__")
	big(r)
	if !defNames(r, 128_000)["mcp__rea__analyze_javascript_application"] {
		t.Fatal("loaded tool lost when its server came back")
	}
	// Called by its exact name, an unloaded tool runs and is loaded.
	if res := r.Run(context.Background(), "mcp__rea__scan_ports", json.RawMessage(`{}`)); res.Output != "ran scan_ports" {
		t.Fatalf("direct call: %+v", res)
	}
	if strings.Join(r.Loaded(), ",") != "mcp__rea__analyze_javascript_application,mcp__rea__scan_ports" {
		t.Fatalf("loaded %v", r.Loaded())
	}
	r.Unload()
	if defNames(r, 128_000)["mcp__rea__scan_ports"] {
		t.Fatal("Unload should defer again")
	}
}
