package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// waitSite waits until the stand-in site has what ok looks for.
func waitSite(t *testing.T, st *site, what string, ok func(*site) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		done := ok(st)
		st.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the site never saw %s", what)
}

// posted finds the last post of chat id with an item matching ok.
func posted(st *site, id string, ok func(relayChat, Item) bool) bool {
	for _, p := range st.relay {
		for _, c := range p.Chats {
			for _, it := range c.Items {
				if c.ID == id && ok(c, it) {
					return true
				}
			}
		}
	}
	return false
}

func noticed(st *site, kind, call string) *relayNotice {
	for _, p := range st.relay {
		for _, n := range p.Notify {
			if n.Kind == kind && n.Call == call {
				return &n
			}
		}
	}
	return nil
}

// With Phone companion on, a chat goes to the relay as it runs: the tool
// call waiting for an answer, a notice for the phone, and the reply. What
// the phone sends back is done: the answer, then a follow-up. Off takes
// the Mac off the relay.
func TestCompanionFollowsAChat(t *testing.T) {
	a, dir, st := appOn(t, "", scripted(
		sse("", "call_1", "bash", `{"command":"ls"}`),
		sse("Done listing.", "", "", ""),
		sse("Again, done.", "", "", ""),
	))
	t.Setenv("CAVEIRA_CONFIRM", "1")
	if a.Companion() {
		t.Fatal("Phone companion is on before anyone turned it on")
	}
	if on, err := a.SetCompanion(true); err != nil || !on {
		t.Fatalf("SetCompanion: %v %v", on, err)
	}

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(v.ID, "list the files"); err != nil {
		t.Fatal(err)
	}
	waitSite(t, st, "the approval", func(st *site) bool {
		return posted(st, v.ID, func(c relayChat, it Item) bool {
			return c.Waiting && c.Running && it.Tool != nil && it.Tool.Call == "call_1" && it.Tool.Status == "approval"
		}) && noticed(st, "approval", "call_1") != nil
	})
	st.mu.Lock()
	if n := noticed(st, "approval", "call_1"); n.Chat != v.ID || !strings.Contains(n.Body, "ls") || n.Title == "" {
		t.Errorf("approval notice %+v", n)
	}
	if !st.relay[0].Chats[0].Snapshot {
		t.Error("the first post of a chat is not a snapshot")
	}
	// The phone allows it.
	st.commands = append(st.commands, relayCommand{ID: 1, Chat: v.ID, Kind: "allow", Call: "call_1"})
	st.mu.Unlock()

	waitSite(t, st, "the reply", func(st *site) bool {
		return posted(st, v.ID, func(c relayChat, it Item) bool {
			return it.Kind == "assistant" && it.Text == "Done listing." && !c.Running && !c.Waiting
		}) && noticed(st, "done", "") != nil
	})
	st.mu.Lock()
	if n := noticed(st, "done", ""); n.Body != "Done listing." {
		t.Errorf("done notice %+v", n)
	}
	// A follow-up from the phone.
	st.commands = append(st.commands, relayCommand{ID: 2, Chat: v.ID, Kind: "message", Text: "and again"})
	st.mu.Unlock()
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running && len(v.Items) == 5 })
	if got := kinds(v.Items); got != "user tool:Run:done assistant user assistant" || v.Items[3].Text != "and again" {
		t.Fatalf("items %q", got)
	}
	waitSite(t, st, "the follow-up's reply", func(st *site) bool {
		return posted(st, v.ID, func(c relayChat, it Item) bool { return it.Text == "Again, done." })
	})

	if on, err := a.SetCompanion(false); err != nil || on {
		t.Fatalf("SetCompanion(false): %v %v", on, err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.forgot != 1 {
		t.Fatalf("turning it off took the Mac off the relay %d times", st.forgot)
	}
	if loadPrefs().Companion {
		t.Fatal("off was not saved")
	}
}

// An empty New chat stays off the phone until its first message.
func TestCompanionSkipsEmptyChats(t *testing.T) {
	a, dir, st := appOn(t, "", scripted(sse("Hi.", "", "", ""), sse("Hello.", "", "", "")))
	if _, err := a.SetCompanion(true); err != nil {
		t.Fatal(err)
	}
	other, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(other.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	empty, err := a.NewChat(dir) // a New chat is reused while it is empty
	if err != nil || empty.ID == other.ID {
		t.Fatalf("NewChat: %v %v", empty.ID, err)
	}
	waitSite(t, st, "the other chat's reply", func(st *site) bool {
		return posted(st, other.ID, func(c relayChat, it Item) bool { return it.Text == "Hi." })
	})
	st.mu.Lock()
	for _, p := range st.relay {
		for _, c := range p.Chats {
			if c.ID == empty.ID {
				t.Errorf("the empty chat went up: %+v", c)
			}
		}
	}
	st.mu.Unlock()

	if _, err := a.Send(empty.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	waitSite(t, st, "the first message", func(st *site) bool {
		return posted(st, empty.ID, func(c relayChat, it Item) bool {
			return c.Snapshot && it.Kind == "user" && it.Text == "hello"
		})
	})
	a.stopAll()
}

// Quitting with the companion on tells the site this Mac is going.
func TestCompanionGoesOfflineOnQuit(t *testing.T) {
	a, _, st := appOn(t, "", nil)
	if _, err := a.SetCompanion(true); err != nil {
		t.Fatal(err)
	}
	if !loadPrefs().Companion {
		t.Fatal("on was not saved")
	}
	a.stopAll()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.offline != 1 {
		t.Fatalf("offline posts %d", st.offline)
	}
}

func TestRelayItemClipsLongOutput(t *testing.T) {
	long := strings.Repeat("é", 4<<10) // two bytes each
	it := relayItem(Item{ID: "i1", Kind: "tool", Tool: &ToolView{Output: long, Diff: "short"}})
	if len(it.Tool.Output) > 4<<10+len("…") || !strings.HasSuffix(it.Tool.Output, "…") || it.Tool.Diff != "short" {
		t.Fatalf("clipped to %d bytes", len(it.Tool.Output))
	}
	if !utf8.ValidString(it.Tool.Output) {
		t.Fatal("cut inside a character")
	}
}
