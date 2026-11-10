package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
)

// sse is one streamed reply: some text, then optionally one tool call.
func sse(text, callID, name, args string) string {
	var sb strings.Builder
	frame := func(v any) {
		b, _ := json.Marshal(v)
		sb.WriteString("data: " + string(b) + "\n\n")
	}
	choice := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	if text != "" {
		// Two fragments, so the window sees an item and then a delta.
		half := len(text) / 2
		frame(choice(map[string]any{"content": text[:half]}, nil))
		frame(choice(map[string]any{"content": text[half:]}, nil))
	}
	finish := "stop"
	if name != "" {
		finish = "tool_calls"
		frame(choice(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": callID, "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		}}}, nil))
	}
	frame(choice(map[string]any{}, finish))
	frame(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 20}})
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

// fakeModel answers each request with the next scripted reply.
func fakeModel(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(scripted(replies...))
	t.Cleanup(srv.Close)
	return srv
}

// scripted is fakeModel's answers, for serving behind the stand-in site.
func scripted(replies ...string) http.Handler {
	var mu sync.Mutex
	n := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(replies) {
			w.WriteHeader(500)
			io.WriteString(w, `{"error":{"message":"no more replies"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, replies[i])
	})
}

// testApp is an App with its own home, pointed at srv, in a project with
// one file in it, signed in on Free to a stand-in for the caveira site.
func testApp(t *testing.T, srv *httptest.Server) (*App, string) {
	t.Helper()
	base := ""
	if srv != nil {
		base = srv.URL
	}
	a, dir, _ := appOn(t, base, nil)
	return a, dir
}

// appOn is testApp, with the stand-in site to hand. Model requests go to
// base, or with base "" through the site to model.
func appOn(t *testing.T, base string, model http.Handler) (*App, string, *site) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("TERM", "test") // no login shell to ask
	for _, k := range []string{"CAVEIRA_API_KEY", "CAVEIRA_MODEL", "CAVEIRA_CONFIRM", "CAVEIRA_REASONING_EFFORT"} {
		t.Setenv(k, "")
	}
	t.Setenv("CAVEIRA_BASE_URL", base)
	st := newSite(t, model)
	t.Setenv("CAVEIRA_CLOUD_URL", st.URL)
	if err := cloud.Save(cloud.Auth{URL: st.URL, Token: st.token, Email: st.email}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewApp("test")
	// Naming would take a scripted reply; names_test.go names chats.
	a.namer = nil
	a.browse = func(string) error { return nil }
	t.Cleanup(a.stopAll)
	if v := a.refreshAccount(); v.Plan != "free" {
		t.Fatalf("not on Free: %+v", v)
	}
	return a, dir, st
}

func waitIdle(t *testing.T, a *App, id string, until func(ChatView) bool) ChatView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, err := a.OpenChat(id)
		if err != nil {
			t.Fatal(err)
		}
		if until(v) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
	return ChatView{}
}

func kinds(items []Item) string {
	var out []string
	for _, it := range items {
		k := it.Kind
		if it.Tool != nil {
			k += ":" + it.Tool.Label + ":" + it.Tool.Status
		}
		out = append(out, k)
	}
	return strings.Join(out, " ")
}

// A turn with a tool call ends up in the transcript the way it happened,
// and a chat opened again from disk reads the same.
func TestTurnBuildsTheTranscript(t *testing.T) {
	srv := fakeModel(t,
		sse("Let me look.", "call_1", "read_file", `{"path":"a.txt"}`),
		sse("It says hi there.", "", "", ""),
	)
	a, dir := testApp(t, srv)

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Problem != "" || v.NeedsKey {
		t.Fatalf("chat cannot run: %+v", v)
	}
	if again, _ := a.NewChat(dir); again.ID != v.ID {
		t.Fatal("a second new chat in the same folder should reuse the empty one")
	}
	if _, err := a.Send(v.ID, "what is in a.txt?"); err != nil {
		t.Fatal(err)
	}
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running })

	want := "user assistant tool:Read:done assistant"
	if got := kinds(v.Items); got != want {
		t.Fatalf("items %q, want %q", got, want)
	}
	if v.Items[1].Text != "Let me look." || v.Items[3].Text != "It says hi there." {
		t.Fatalf("assistant text: %q, %q", v.Items[1].Text, v.Items[3].Text)
	}
	tool := v.Items[2].Tool
	if tool.Preview != "a.txt" || !strings.Contains(tool.Output, "hi there") || tool.Summary == "" {
		t.Fatalf("tool: %+v", tool)
	}
	if v.Title != "what is in a.txt?" || v.Context == 0 {
		t.Fatalf("title %q, context %d", v.Title, v.Context)
	}

	headers, err := a.Chats(dir)
	if err != nil || len(headers) != 1 || headers[0].ID != v.ID {
		t.Fatalf("chats: %+v, %v", headers, err)
	}
	// Every project's, for the sidebar's folders: each says whose it is.
	all, err := a.Chats("")
	if err != nil || len(all) != 1 || all[0].ID != v.ID || all[0].Dir != dir {
		t.Fatalf("all chats: %+v, %v", all, err)
	}

	// Forget it and read it back from disk.
	a.mu.Lock()
	delete(a.chats, v.ID)
	a.mu.Unlock()
	back, err := a.OpenChat(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(back.Items); got != want {
		t.Fatalf("reopened items %q, want %q", got, want)
	}
	if back.Items[2].Tool.Preview != "a.txt" || !strings.Contains(back.Items[2].Tool.Output, "hi there") {
		t.Fatalf("reopened tool: %+v", back.Items[2].Tool)
	}

	if err := a.DeleteChat(v.ID); err != nil {
		t.Fatal(err)
	}
	if headers, _ := a.Chats(dir); len(headers) != 0 {
		t.Fatalf("deleted chat still listed: %+v", headers)
	}
}

// Under confirm, a command waits in the transcript for an answer.
func TestApprovalWaitsForAnAnswer(t *testing.T) {
	srv := fakeModel(t,
		sse("", "call_1", "bash", `{"command":"ls"}`),
		sse("Done.", "", "", ""),
	)
	a, dir := testApp(t, srv)
	t.Setenv("CAVEIRA_CONFIRM", "1")

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(v.ID, "list the files"); err != nil {
		t.Fatal(err)
	}
	v = waitIdle(t, a, v.ID, func(v ChatView) bool {
		return len(v.Items) > 1 && v.Items[len(v.Items)-1].Tool != nil && v.Items[len(v.Items)-1].Tool.Status == "approval"
	})
	if _, err := a.Send(v.ID, "again"); err == nil {
		t.Fatal("sent a message while a turn was running")
	}
	a.Answer(v.ID, "call_1", "allow")
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running })
	if got := kinds(v.Items); got != "user tool:Run:done assistant" {
		t.Fatalf("items %q", got)
	}
	if !strings.Contains(v.Items[1].Tool.Output, "a.txt") {
		t.Fatalf("ls output: %q", v.Items[1].Tool.Output)
	}
}

// Signed out there is nowhere to send, and a message waits under a sign-in
// instead of running. A key of one's own counts for nothing, a hosted
// server is refused in one line, and a local one is ready.
func TestNoKeyIsAProblemNotACrash(t *testing.T) {
	a, dir := testApp(t, nil)
	if _, err := a.SignOut(); err != nil {
		t.Fatal(err)
	}
	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !v.NeedsKey {
		t.Fatalf("want NeedsKey: %+v", v)
	}
	if started, err := a.Send(v.ID, "hi"); err != nil || started {
		t.Fatalf("send without a key: started %v, %v", started, err)
	}
	v, _ = a.OpenChat(v.ID)
	if got := kinds(v.Items); got != "user paywall" || v.Items[1].Text != "" {
		t.Fatalf("want it held for a sign-in: %q %+v", got, v.Items)
	}
	if a.Settings().Ready {
		t.Fatal("settings say ready without a key")
	}

	t.Setenv("CAVEIRA_API_KEY", "local-key-for-test")
	if a.Settings().Ready {
		t.Fatal("a key in the environment made it ready")
	}

	t.Setenv("CAVEIRA_BASE_URL", "https://api.openai.com/v1")
	if a.Settings().Ready {
		t.Fatal("a hosted server made it ready")
	}
	h, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h.NeedsKey || !strings.HasPrefix(h.Problem, "CAVEIRA_BASE_URL: api.openai.com is not on this machine or your network") || strings.Contains(h.Problem, "\n") {
		t.Fatalf("hosted server: %+v", h)
	}
	if _, err := a.Send(h.ID, "hi"); err == nil || err.Error() != h.Problem {
		t.Fatalf("sent to a hosted server: %v", err)
	}

	t.Setenv("CAVEIRA_BASE_URL", "http://localhost:11434/v1")
	if !a.Settings().Ready {
		t.Fatal("a local server was not seen")
	}
	if _, err := a.SaveSettings(SettingsInput{Model: "glm-5.2", Confirm: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".caveira", "config.json"))
	if strings.Contains(string(b), "ak_test") || strings.Contains(string(b), "base_url") || !strings.Contains(string(b), "glm-5.2") {
		t.Fatalf("config.json: %s", b)
	}
}

// Signed out, a chat on a local model server runs, with no token sent and
// no sign-in asked for, and its models are the server's.
func TestLocalServerRunsSignedOut(t *testing.T) {
	var auth atomic.Value
	reply := scripted(sse("Local hello.", "", "", ""))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"qwen3:8b"}]}`))
			return
		}
		reply.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	a, dir := testApp(t, srv)
	if _, err := a.SignOut(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "ak_should_not_go")
	if !a.Settings().Ready {
		t.Fatal("a local server is not ready")
	}
	if models, err := a.Models(); err != nil || len(models) != 1 || models[0].ID != "qwen3:8b" {
		t.Fatalf("models: %+v, %v", models, err)
	}
	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.NeedsKey || v.Problem != "" {
		t.Fatalf("local chat: %+v", v)
	}
	if started, err := a.Send(v.ID, "hi"); err != nil || !started {
		t.Fatalf("send: started %v, %v", started, err)
	}
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running && len(v.Items) == 2 })
	if got := kinds(v.Items); got != "user assistant" {
		t.Fatalf("items %q", got)
	}
	if got, _ := auth.Load().(string); got != "" {
		t.Fatalf("the local server got Authorization %q", got)
	}
}

// A reply that turns out to be a tool call written as text leaves no
// empty bubble behind.
func TestWrittenOutToolCallLeavesNoBubble(t *testing.T) {
	c := newChat("c", "/tmp", nil)
	c.apply(agent.TextEvent{Delta: `{"name":"read_file",`})
	out := c.apply(agent.AssistantDoneEvent{Message: llm.Message{Role: llm.RoleAssistant}})
	if len(out) != 1 || out[0].Type != "remove" || len(c.items) != 0 {
		t.Fatalf("events %+v, items %+v", out, c.items)
	}
	c.apply(agent.ErrorEvent{Err: errors.New("boom")})
	if len(c.items) != 1 || c.items[0].Tone != "error" {
		t.Fatalf("items %+v", c.items)
	}
}

// A busy model: the reply that failed goes, one notice counts down while
// the request waits to go again and is gone once it does, and a turn that
// gave up offers Try again.
func TestBusyModelNotice(t *testing.T) {
	c := newChat("c", "/tmp", nil)
	c.apply(agent.TextEvent{Delta: "Partial ans"})
	until := time.Now().Add(23 * time.Second)
	out := c.apply(agent.RetryEvent{Reason: llm.RateLimited, Try: 1, Wait: 23 * time.Second, Until: until})
	if len(out) != 2 || out[0].Type != "remove" || out[1].Item == nil || out[1].Item.Until != until.UnixMilli() {
		t.Fatalf("events %+v", out)
	}
	c.apply(agent.RetryEvent{Reason: llm.RateLimited, Try: 2, Wait: 40 * time.Second, Until: until.Add(40 * time.Second)})
	if len(c.items) != 1 || c.items[0].Kind != "notice" || c.items[0].Text != "Rate limited" || c.items[0].Until != until.Add(40*time.Second).UnixMilli() {
		t.Fatalf("items %+v", c.items)
	}
	c.apply(agent.TextEvent{Delta: "Full answer."})
	if len(c.items) != 1 || c.items[0].Kind != "assistant" || c.items[0].Text != "Full answer." {
		t.Fatalf("items %+v", c.items)
	}
	c.apply(agent.ErrorEvent{Err: &agent.GaveUpError{Reason: llm.RateLimited, For: 15 * time.Minute, Tries: 12, Err: errors.New("slow down")}})
	if last := c.items[len(c.items)-1]; last.Action != "retry" || !strings.HasPrefix(last.Text, "Still rate limited after 15m") {
		t.Fatalf("last item %+v", last)
	}
}

// The caveira site's own wait shows in its words in the notice.
func TestSiteWaitNoticeSaysTheSitesWords(t *testing.T) {
	c := newChat("c", "/tmp", nil)
	site := &llm.APIError{Status: 429, Type: "caveira", Code: "too_many_running", Message: "3 requests already running on this account, wait a moment"}
	c.apply(agent.RetryEvent{Reason: llm.AccountBusy, Try: 1, Wait: 5 * time.Second, Until: time.Now().Add(5 * time.Second), Err: site})
	if len(c.items) != 1 || c.items[0].Kind != "notice" || c.items[0].Text != site.Message {
		t.Fatalf("items %+v", c.items)
	}
}

func TestParseEnvSkipsWhatTheProfilePrints(t *testing.T) {
	out := []byte("welcome back!\n" + envMarker + "PATH=/opt/homebrew/bin:/usr/bin\x00MULTI=a\nb\x00junk\x00")
	env := parseEnv(out)
	if env["PATH"] != "/opt/homebrew/bin:/usr/bin" || env["MULTI"] != "a\nb" || len(env) != 2 {
		t.Fatalf("env %v", env)
	}
	if parseEnv([]byte("no marker")) != nil {
		t.Fatal("parsed output without the marker")
	}
}

func TestProposedShowsWhatAnEditWillDo(t *testing.T) {
	got := proposed("edit_file", json.RawMessage(`{"path":"a.go","old_string":"a\nb\n","new_string":"c"}`))
	if got != "-a\n-b\n+c" {
		t.Fatalf("edit: %q", got)
	}
	if got := proposed("write_file", json.RawMessage(`{"path":"a.go","content":"x\ny"}`)); got != "+x\n+y" {
		t.Fatalf("write: %q", got)
	}
	if got := proposed("bash", json.RawMessage(`{"command":"ls"}`)); got != "" {
		t.Fatalf("bash: %q", got)
	}
}

// Bypassing permissions mid-turn lets the command already waiting run,
// and the choice is what new chats start with.
func TestBypassAnswersWhatIsWaiting(t *testing.T) {
	srv := fakeModel(t,
		sse("", "call_1", "bash", `{"command":"ls"}`),
		sse("Done.", "", "", ""),
	)
	a, dir := testApp(t, srv)
	t.Setenv("CAVEIRA_CONFIRM", "1")

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Ask {
		t.Fatal("confirm is on, but the chat does not ask")
	}
	if _, err := a.Send(v.ID, "list the files"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, v.ID, func(v ChatView) bool {
		n := len(v.Items)
		return n > 1 && v.Items[n-1].Tool != nil && v.Items[n-1].Tool.Status == "approval"
	})
	if v, err = a.SetPermissions(v.ID, false); err != nil || v.Ask {
		t.Fatalf("bypass: ask %v, %v", v.Ask, err)
	}
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running })
	if got := kinds(v.Items); got != "user tool:Run:done assistant" {
		t.Fatalf("items %q", got)
	}
	file, _ := config.ReadFile()
	if file.Confirm {
		t.Fatal("bypassing was not kept for new chats")
	}
	if v, _ = a.SetPermissions(v.ID, true); !v.Ask {
		t.Fatal("asking again did not take")
	}
	if file, _ = config.ReadFile(); !file.Confirm {
		t.Fatal("asking was not kept for new chats")
	}
}
