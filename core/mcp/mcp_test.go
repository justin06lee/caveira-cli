package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
	"github.com/justin06lee/caveira/core/tools"
)

// The test binary doubles as a tiny stdio MCP server: run with
// CAVEIRA_TEST_MCP_SERVER=1 it serves an echo tool and a failing one.
func TestMain(m *testing.M) {
	if os.Getenv("CAVEIRA_TEST_MCP_SERVER") == "1" {
		// Started as a managed rea, the first argument is the version's
		// script (rea_test.go); one that says so is a broken release.
		if len(os.Args) > 1 {
			if b, err := os.ReadFile(os.Args[1]); err == nil && strings.Contains(string(b), "broken") {
				fmt.Fprintln(os.Stderr, "rea: this build is broken")
				os.Exit(1)
			}
		}
		serve()
		return
	}
	os.Exit(m.Run())
}

func serve() {
	s := sdk.NewServer(&sdk.Implementation{Name: "testserver", Version: "v0.0.1"}, nil)
	type echoIn struct {
		Message string `json:"message" jsonschema:"what to say back"`
	}
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Says the message back.\nSecond line."},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + in.Message + " in " + mustWd()}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "big", Description: "A lot of output."},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat("x\n", 50_000)}}}, nil, nil
		})
	if err := s.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mustWd() string {
	wd, _ := os.Getwd()
	return filepath.Base(wd)
}

func home(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
}

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	sub := filepath.Join(dir, "pkg")
	os.Mkdir(sub, 0o755)
	return sub
}

func TestProjectConfigMergesOverUser(t *testing.T) {
	home(t)
	work := project(t)
	writeJSON(t, UserPath(), `{"mcpServers":{
		"files":{"command":"npx","args":["-y","files"]},
		"remote":{"type":"http","url":"https://u.example/mcp","headers":{"Authorization":"Bearer u"}}}}`)
	writeJSON(t, ProjectPath(work), `{"mcpServers":{
		"remote":{"url":"https://p.example/mcp"},
		"local":{"command":"./server","disabled":true}}}`)

	got, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "files" || got[1].Name != "local" || got[2].Name != "remote" {
		t.Fatalf("entries: %+v", got)
	}
	if got[0].Scope != User || got[0].Transport() != "stdio" || got[0].Summary() != "npx -y files" {
		t.Fatalf("files: %+v", got[0])
	}
	if !got[1].Disabled || got[1].Scope != Project {
		t.Fatalf("local: %+v", got[1])
	}
	// The project's entry replaces the user's whole, headers and all.
	if got[2].Scope != Project || got[2].URL != "https://p.example/mcp" || got[2].Headers != nil || got[2].Transport() != "http" {
		t.Fatalf("remote: %+v", got[2])
	}

	// Enabling and removing edit the file the server is in.
	if err := SetEnabled(work, "local", true); err != nil {
		t.Fatal(err)
	}
	p, _ := ReadFile(ProjectPath(work))
	if p.Servers["local"].Disabled {
		t.Fatal("local still disabled")
	}
	if err := Remove(work, "files"); err != nil {
		t.Fatal(err)
	}
	u, _ := ReadFile(UserPath())
	if _, ok := u.Servers["files"]; ok || len(u.Servers) != 1 {
		t.Fatalf("user file after remove: %+v", u)
	}
	if err := Remove(work, "nope"); err == nil {
		t.Fatal("removed a server that is not there")
	}
	if fi, _ := os.Stat(UserPath()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.json mode %v", fi.Mode())
	}
}

func TestBrokenConfigSaysWhere(t *testing.T) {
	home(t)
	writeJSON(t, UserPath(), `{"mcpServers": {`)
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "mcp.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddChecks(t *testing.T) {
	home(t)
	if err := Add(User, "", "bad name", Server{Command: "x"}); err == nil {
		t.Fatal("took a name with a space")
	}
	if err := Add(User, "", "a__b", Server{Command: "x"}); err == nil {
		t.Fatal("took a name with __")
	}
	if err := Add(User, "", "web", Server{Type: "http", URL: "ftp://x"}); err == nil {
		t.Fatal("took a non-http url")
	}
	if err := Add(User, "", "ok", Server{Command: "uvx", Args: []string{"thing"}}); err != nil {
		t.Fatal(err)
	}
}

func TestExpand(t *testing.T) {
	t.Setenv("CAV_TEST_TOKEN", "sekrit")
	for in, want := range map[string]string{
		"Bearer ${CAV_TEST_TOKEN}":   "Bearer sekrit",
		"${CAV_TEST_MISSING:-dflt}":  "dflt",
		"${CAV_TEST_MISSING}":        "",
		"$CAV_TEST_TOKEN stays":      "$CAV_TEST_TOKEN stays",
		"no vars at all":             "no vars at all",
		"a ${CAV_TEST_TOKEN} b ${X}": "a sekrit b ",
	} {
		if got := expand(in, nil); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolNames(t *testing.T) {
	taken := map[string]bool{}
	if got := toolName("github", "create_issue", taken); got != "mcp__github__create_issue" {
		t.Fatal(got)
	}
	if got := toolName("my.server", "do thing/now", taken); got != "mcp__my_server__do_thing_now" {
		t.Fatal(got)
	}
	long := toolName("server", strings.Repeat("t", 80), taken)
	if len(long) > 64 || !strings.HasPrefix(long, "mcp__server__ttt") {
		t.Fatalf("long name %q (%d)", long, len(long))
	}
	taken["mcp__a__b"] = true
	if got := toolName("a", "b", taken); got == "mcp__a__b" || len(got) > 64 {
		t.Fatalf("clash not resolved: %q", got)
	}
	if s, tool, ok := SplitName("mcp__github__create_issue"); !ok || s != "github" || tool != "create_issue" {
		t.Fatal("SplitName")
	}
	if _, _, ok := SplitName("bash"); ok {
		t.Fatal("SplitName took bash")
	}
}

// selfServer is a configuration that runs this test binary as a server.
func selfServer() string {
	b, _ := json.Marshal(Server{Command: os.Args[0], Env: map[string]string{"CAVEIRA_TEST_MCP_SERVER": "1"}})
	return string(b)
}

func started(t *testing.T, work string) *Manager {
	t.Helper()
	m := NewManager(work, "test")
	m.Start()
	t.Cleanup(m.Close)
	m.Wait(context.Background(), 30*time.Second)
	return m
}

// End to end: a stdio server starts, lists its tools, and runs a call; a
// server that cannot start shows as failed with why, and the rest work.
func TestStdioServerEndToEnd(t *testing.T) {
	home(t)
	work := project(t)
	writeJSON(t, UserPath(), `{"mcpServers":{
		"test":`+selfServer()+`,
		"broken":{"command":"/nonexistent/caveira-mcp-server"},
		"off":{"command":"whatever","disabled":true}}}`)
	m := started(t, work)

	states := map[string]State{}
	for _, s := range m.States() {
		states[s.Name] = s
	}
	if s := states["test"]; s.Status != Ready || len(s.Tools) != 2 || s.Tools[1].Name != "echo" || s.Tools[1].Description != "Says the message back." {
		t.Fatalf("test server: %+v", s)
	}
	if s := states["broken"]; s.Status != Failed || !strings.Contains(s.Error, "nonexistent") {
		t.Fatalf("broken server: %+v", s)
	}
	if s := states["off"]; s.Status != Off {
		t.Fatalf("disabled server: %+v", s)
	}

	reg := tools.Default(work)
	m.Install(reg)
	tool, ok := reg.Get("mcp__test__echo")
	if !ok {
		t.Fatalf("echo not registered: %v", reg.Definitions(0))
	}
	if tool.Kind() != tools.KindExecute {
		t.Fatal("MCP tools must ask like commands")
	}
	sch := tool.Schema()
	if sch["type"] != "object" || sch["properties"].(map[string]any)["message"] == nil {
		t.Fatalf("schema: %v", sch)
	}
	res := reg.Run(context.Background(), "mcp__test__echo", json.RawMessage(`{"message":"hi"}`))
	// The server runs in the working directory.
	if res.IsError || res.Output != "echo: hi in pkg" {
		t.Fatalf("echo: %+v", res)
	}
	// Output is capped like a command's.
	big := reg.Run(context.Background(), "mcp__test__big", json.RawMessage(`{}`))
	if len(big.Output) > tools.OutputLimit+200 || !strings.Contains(big.Output, "truncated") {
		t.Fatalf("big output is %d bytes", len(big.Output))
	}

	// Disabled in the file, Start again stops it and its tools go.
	if err := SetEnabled(work, "test", false); err != nil {
		t.Fatal(err)
	}
	m.Start()
	m.Install(reg)
	if _, ok := reg.Get("mcp__test__echo"); ok {
		t.Fatal("a disabled server's tool is still offered")
	}
	if _, ok := reg.Get("bash"); !ok {
		t.Fatal("built-in tools went with it")
	}
}

// sse is one streamed model reply: an optional tool call, or text.
func sse(text string, call *llm.ToolCall) string {
	var sb strings.Builder
	frame := func(v any) {
		b, _ := json.Marshal(v)
		sb.WriteString("data: " + string(b) + "\n\n")
	}
	if text != "" {
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
	}
	finish := "stop"
	if call != nil {
		finish = "tool_calls"
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments}}},
		}}}})
	}
	frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

// model replies with the call each turn, then text after its result.
func model(t *testing.T, call llm.ToolCall) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		n++
		odd := n%2 == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if odd {
			io.WriteString(w, sse("", &call))
		} else {
			io.WriteString(w, sse("done", nil))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An MCP call waits for approval like a command; "always" allows the
// server in this project, on disk, so a new session does not ask again.
func TestMCPCallsGoThroughApproval(t *testing.T) {
	home(t)
	work := project(t)
	writeJSON(t, UserPath(), `{"mcpServers":{"test":`+selfServer()+`}}`)
	m := started(t, work)
	call := llm.ToolCall{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "mcp__test__echo", Arguments: `{"message":"yo"}`}}
	srv := model(t, call)

	newAgent := func() *agent.Agent {
		ag := agent.New(llm.New(srv.URL, ""), config.Settings{Model: "m", Confirm: true}, work, "sys")
		ag.Prepare = m.Prepare(ag.Tools)
		return ag
	}
	turn := func(ag *agent.Agent, d agent.Decision) (asked []agent.ApprovalEvent, result tools.Result) {
		ag.Run(context.Background(), "go", func(ev agent.Event) {
			switch ev := ev.(type) {
			case agent.ApprovalEvent:
				asked = append(asked, ev)
				ev.Reply <- d
			case agent.ToolEndEvent:
				result = ev.Result
			}
		})
		return
	}

	ag := newAgent()
	asked, res := turn(ag, agent.Deny)
	if len(asked) != 1 || asked[0].Name != "mcp__test__echo" || asked[0].Always != "the test server in this project" || !strings.Contains(asked[0].Preview, `test · echo {"message":"yo"}`) {
		t.Fatalf("approval: %+v", asked)
	}
	if !res.IsError || res.Summary != "denied" {
		t.Fatalf("denied call ran: %+v", res)
	}

	asked, res = turn(ag, agent.Allow)
	if len(asked) != 1 || res.IsError || !strings.HasPrefix(res.Output, "echo: yo") {
		t.Fatalf("allowed: %d asks, %+v", len(asked), res)
	}

	asked, _ = turn(ag, agent.AllowAlways)
	if len(asked) != 1 || !Allowed(work, "test") {
		t.Fatalf("always: %d asks, allowed %v", len(asked), Allowed(work, "test"))
	}
	// Another session in the same project runs it without asking.
	asked, res = turn(newAgent(), agent.Deny)
	if len(asked) != 0 || res.IsError {
		t.Fatalf("after always: %d asks, %+v", len(asked), res)
	}
	// Another project still asks.
	if Allowed(project(t), "test") {
		t.Fatal("always leaked to another project")
	}
}

func TestSeedDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	bin := t.TempDir()
	t.Setenv("PATH", bin)

	// Without npx nothing is added, and rea waits for a later start.
	if err := SeedDefaults(); err != nil {
		t.Fatal(err)
	}
	if f, _ := ReadFile(UserPath()); len(f.Servers) != 0 {
		t.Fatalf("seeded without npx: %v", f.Servers)
	}

	npx := "npx"
	if runtime.GOOS == "windows" {
		npx = "npx.exe" // found through PATHEXT; what is in it does not matter
	}
	if err := os.WriteFile(filepath.Join(bin, npx), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(); err != nil {
		t.Fatal(err)
	}
	f, _ := ReadFile(UserPath())
	if s, ok := f.Servers["rea"]; !ok || s.Command != "npx" || s.Args[len(s.Args)-1] != "mcp" {
		t.Fatalf("rea not seeded: %v", f.Servers)
	}

	// Removed once, it stays removed.
	if err := Remove(t.TempDir(), "rea"); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(); err != nil {
		t.Fatal(err)
	}
	if f, _ := ReadFile(UserPath()); len(f.Servers) != 0 {
		t.Fatalf("rea came back after removal: %v", f.Servers)
	}
}
