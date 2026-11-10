package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/llm"
)

// The name is asked of the small model, without reasoning or tools, and
// comes back without the quotes and full stop it was given with.
func TestNameChatAsksTheSmallModel(t *testing.T) {
	var got struct {
		Model    string        `json:"model"`
		Effort   string        `json:"reasoning_effort"`
		Tools    []any         `json:"tools"`
		Messages []llm.Message `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`"Reading a.txt."`, "", "", ""))
	}))
	defer srv.Close()

	name, err := nameChat(context.Background(), llm.New(srv.URL, "k"), "what is in a.txt?")
	if err != nil {
		t.Fatal(err)
	}
	if name != "Reading a.txt" {
		t.Fatalf("name %q", name)
	}
	if got.Model != "qwen-3.5" || got.Effort != "none" || len(got.Tools) != 0 {
		t.Fatalf("request: model %q, effort %q, %d tools", got.Model, got.Effort, len(got.Tools))
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "what is in a.txt?" {
		t.Fatalf("messages: %+v", got.Messages)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the login bug":                       "Fix the login bug",
		"  **Title: Fix the login bug.**\n":       "Fix the login bug",
		"<think>a bug, then</think>\n\nLogin bug": "Login bug",
		"“Poppins everywhere”":                    "Poppins everywhere",
		"":                                        "",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A new chat takes the name the model gives it, whether the name comes
// while the first turn runs or after it ends; it is kept on disk, told to
// the window, and asked for only once.
func TestChatIsNamedByTheModel(t *testing.T) {
	for _, during := range []bool{true, false} {
		t.Run(map[bool]string{true: "during the turn", false: "after the turn"}[during], func(t *testing.T) {
			var a *App
			var id string
			named := func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return a.chats[id].named
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				// Held until the name is in, so it lands mid-turn.
				for during && !named() {
					time.Sleep(5 * time.Millisecond)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse("Hi.", "", "", ""))
			}))
			defer srv.Close()
			a, dir := testApp(t, srv)

			var mu sync.Mutex
			var titles []string
			a.sink = func(name string, data any) {
				if e, ok := data.(ChatEvent); ok && e.Type == "title" {
					mu.Lock()
					titles = append(titles, e.Title)
					mu.Unlock()
				}
			}
			gate := make(chan struct{})
			asked := make(chan string, 4)
			a.namer = func(ctx context.Context, client *llm.Client, first string) (string, error) {
				asked <- first
				if !during {
					<-gate
				}
				return "Saying hi", nil
			}

			v, err := a.NewChat(dir)
			if err != nil {
				t.Fatal(err)
			}
			id = v.ID
			if _, err := a.Send(id, "say hi\nplease"); err != nil {
				t.Fatal(err)
			}
			v = waitIdle(t, a, id, func(v ChatView) bool { return !v.Running })
			if !during {
				if v.Title != "say hi" {
					t.Fatalf("before the name, title %q", v.Title)
				}
				close(gate)
			}
			v = waitIdle(t, a, id, func(v ChatView) bool { return v.Title == "Saying hi" })
			if first := <-asked; first != "say hi\nplease" {
				t.Fatalf("named from %q", first)
			}

			headers, err := a.Chats(dir)
			if err != nil || len(headers) != 1 || headers[0].Title != "Saying hi" {
				t.Fatalf("on disk: %+v, %v", headers, err)
			}
			mu.Lock()
			if len(titles) != 1 || titles[0] != "Saying hi" {
				t.Fatalf("title events %q", titles)
			}
			mu.Unlock()

			// A second turn keeps the name and does not ask again.
			if _, err := a.Send(id, "again"); err != nil {
				t.Fatal(err)
			}
			v = waitIdle(t, a, id, func(v ChatView) bool { return !v.Running })
			if v.Title != "Saying hi" || len(asked) != 0 {
				t.Fatalf("second turn: title %q, asked %d more", v.Title, len(asked))
			}
		})
	}
}

// oldChat saves a chat from before names, going by its first line.
func oldChat(t *testing.T, dir, first string) string {
	t.Helper()
	s := &agent.Session{
		ID:        "20261009-120000-abcdef",
		WorkDir:   dir,
		Title:     agent.FirstLineTitle(first),
		CreatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: first},
			{Role: llm.RoleAssistant, Content: "Sure."},
		},
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

// titles records the title events told to the window.
func titles(a *App) func() []string {
	var mu sync.Mutex
	var got []string
	a.sink = func(name string, data any) {
		if e, ok := data.(ChatEvent); ok && e.Type == "title" {
			mu.Lock()
			got = append(got, e.Title)
			mu.Unlock()
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// An older chat still going by its first line is named as it is opened,
// once; one with a name of its own is left alone.
func TestOldChatIsNamedWhenOpened(t *testing.T) {
	a, dir := testApp(t, nil)
	told := titles(a)
	asked := make(chan string, 4)
	a.namer = func(ctx context.Context, client *llm.Client, first string) (string, error) {
		asked <- first
		return "Fixing the build", nil
	}
	id := oldChat(t, dir, "ok so first of all the build is broken\nhere is the log")

	waitIdle(t, a, id, func(v ChatView) bool { return v.Title == "Fixing the build" })
	if first := <-asked; first != "ok so first of all the build is broken\nhere is the log" {
		t.Fatalf("named from %q", first)
	}
	if s, err := agent.LoadSession(id); err != nil || s.Title != "Fixing the build" {
		t.Fatalf("on disk: %+v, %v", s, err)
	}
	if got := told(); len(got) != 1 || got[0] != "Fixing the build" {
		t.Fatalf("title events %q", got)
	}
	if _, err := a.OpenChat(id); err != nil || len(asked) != 0 {
		t.Fatalf("opened again: asked %d more, %v", len(asked), err)
	}
}

// A name given by hand is kept, on disk and in the window, whether the
// chat is open or not, and the model never overwrites it.
func TestRenameChat(t *testing.T) {
	a, dir := testApp(t, nil)
	told := titles(a)
	id := oldChat(t, dir, "hello there")

	if err := a.RenameChat(id, "   "); err == nil {
		t.Fatal("renamed to nothing")
	}
	if err := a.RenameChat(id, "  Saying\n hello  "); err != nil {
		t.Fatal(err)
	}
	if s, _ := agent.LoadSession(id); s.Title != "Saying hello" {
		t.Fatalf("unopened, on disk %q", s.Title)
	}

	// Opened while the model is still thinking of a name: the hand-given
	// one wins.
	a2, dir2 := testApp(t, nil)
	told2 := titles(a2)
	id2 := oldChat(t, dir2, "hello there")
	gate := make(chan struct{})
	a2.namer = func(ctx context.Context, client *llm.Client, first string) (string, error) {
		<-gate
		return "Model name", nil
	}
	if _, err := a2.OpenChat(id2); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 100)
	if err := a2.RenameChat(id2, long); err != nil {
		t.Fatal(err)
	}
	close(gate)
	time.Sleep(50 * time.Millisecond)
	want := strings.Repeat("é", 80)
	if v, _ := a2.OpenChat(id2); v.Title != want {
		t.Fatalf("open, title %q", v.Title)
	}
	if s, _ := agent.LoadSession(id2); s.Title != want {
		t.Fatalf("open, on disk %q", s.Title)
	}
	if got := told(); len(got) != 1 || got[0] != "Saying hello" {
		t.Fatalf("title events %q", got)
	}
	if got := told2(); len(got) != 1 || got[0] != want {
		t.Fatalf("title events %q", got)
	}
}
