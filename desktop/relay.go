package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/justin06lee/caveira/core/cloud"
)

// The relay on the caveira site passes the open chats to the iPhone app
// while Phone companion is on, and what the phone sends back: an answer to
// an approval, a follow-up, a stop. It is plain, not end-to-end encrypted:
// the site keeps the open chats until the companion is turned off.

// relayEvery is how often what changed is posted; relaySnapshot is how many
// of a chat's last items go up the first time it is posted in a run.
const (
	relayEvery    = 700 * time.Millisecond
	relaySnapshot = 200
	relayWait     = 25 // seconds the site holds a commands request open
)

// relay is the companion running: what changed since the last post, and the
// loops that post it and take commands.
type relay struct {
	mu sync.Mutex
	// dirty is, by chat, the items changed since the last post; a chat in
	// it with no items has only its header to post.
	dirty map[string]map[string]bool
	// posted is the chats posted in full this run.
	posted map[string]bool
	// asked is the approvals the phone was told about, by call.
	asked  map[string]bool
	notify []relayNotice
	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

type relayNotice struct {
	Kind  string `json:"kind"` // approval, done
	Chat  string `json:"chat"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Call  string `json:"call,omitempty"`
}

type relayChat struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Dir      string   `json:"dir"`
	Running  bool     `json:"running"`
	Waiting  bool     `json:"waiting"`
	Snapshot bool     `json:"snapshot,omitempty"`
	Items    []Item   `json:"items,omitempty"`
	Removed  []string `json:"removed,omitempty"`
}

type relayCommand struct {
	ID   int64  `json:"id"`
	Chat string `json:"chat"`
	Kind string `json:"kind"` // allow, always, deny, message, stop
	Call string `json:"call,omitempty"`
	Text string `json:"text,omitempty"`
}

// Companion says whether Phone companion is on.
func (a *App) Companion() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs.Companion
}

// SetCompanion turns Phone companion on or off. Off takes this Mac and its
// chats off the relay.
func (a *App) SetCompanion(on bool) (bool, error) {
	a.mu.Lock()
	a.prefs.Companion = on
	err := a.prefs.save()
	a.mu.Unlock()
	if err != nil {
		return !on, err
	}
	if on {
		a.startRelay()
		return true, nil
	}
	a.stopRelay(false)
	auth := a.relayAuth()
	if auth.Token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := relayCall(ctx, http.MethodDelete, auth, "/api/relay/device", nil, nil); err != nil {
			log.Printf("relay: taking this Mac off: %v", err)
		}
	}
	return false, nil
}

// startRelay starts the companion's loops, with every open chat to post.
func (a *App) startRelay() {
	ctx, cancel := context.WithCancel(context.Background())
	r := &relay{
		dirty: map[string]map[string]bool{}, posted: map[string]bool{}, asked: map[string]bool{},
		wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}
	if !a.relay.CompareAndSwap(nil, r) {
		cancel()
		return
	}
	a.mu.Lock()
	for id := range a.chats {
		r.mark(id, "")
	}
	a.mu.Unlock()
	go func() {
		defer close(r.done)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a.postLoop(ctx, r) }()
		go func() { defer wg.Done(); a.commandLoop(ctx, r) }()
		wg.Wait()
	}()
}

// stopRelay stops the loops; offline tells the site this Mac is going, as
// the app quits.
func (a *App) stopRelay(offline bool) {
	r := a.relay.Swap(nil)
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
	if auth := a.relayAuth(); offline && auth.Token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = relayCall(ctx, http.MethodPost, auth, "/api/relay/publish", map[string]bool{"offline": true}, nil)
	}
}

// relayAuth is the account to post as; no token while signed out.
func (a *App) relayAuth() cloud.Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.acct.signedIn() {
		return cloud.Auth{}
	}
	return a.acct.auth
}

// relayTouch marks a chat to post, as it opens.
func (a *App) relayTouch(id string) {
	if r := a.relay.Load(); r != nil {
		r.mark(id, "")
	}
}

// relayNote is every chat event on its way to the window: what it changed
// goes up with the next post. It never takes App.mu, so it is safe where
// that is held.
func (a *App) relayNote(name string, data any) {
	r := a.relay.Load()
	e, ok := data.(ChatEvent)
	if r == nil || !ok || name != "chat" {
		return
	}
	switch e.Type {
	case "usage":
	case "item":
		r.mark(e.Chat, e.Item.ID)
		if t := e.Item.Tool; t != nil && t.Status == "approval" {
			r.mu.Lock()
			if !r.asked[t.Call] {
				r.asked[t.Call] = true
				body := t.Label
				if t.Preview != "" {
					body += " " + t.Preview
				}
				r.notify = append(r.notify, relayNotice{Kind: "approval", Chat: e.Chat, Body: clip(body, 180), Call: t.Call})
			}
			r.mu.Unlock()
		}
	case "delta", "remove":
		r.mark(e.Chat, e.ID)
	case "done":
		r.mark(e.Chat, "")
		if !e.Interrupted {
			r.mu.Lock()
			r.notify = append(r.notify, relayNotice{Kind: "done", Chat: e.Chat})
			r.mu.Unlock()
		}
	default:
		r.mark(e.Chat, "")
	}
}

func (r *relay) mark(chat, item string) {
	r.mu.Lock()
	if r.dirty[chat] == nil {
		r.dirty[chat] = map[string]bool{}
	}
	if item != "" {
		r.dirty[chat][item] = true
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// postLoop posts what changed, at most every relayEvery, backing off while
// the site can't be reached.
func (a *App) postLoop(ctx context.Context, r *relay) {
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(relayEvery, wait)):
		}
		if err := a.post(ctx, r); err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = backoff(wait)
			log.Printf("relay: posting: %v", err)
			// Try again; what failed is still marked.
			select {
			case r.wake <- struct{}{}:
			default:
			}
			continue
		}
		wait = 0
	}
}

// post sends one batch of what changed. On failure it marks it all again.
func (a *App) post(ctx context.Context, r *relay) error {
	auth := a.relayAuth()
	if auth.Token == "" {
		return nil // signed out: kept until a sign-in, and posted then
	}
	r.mu.Lock()
	dirty, notify := r.dirty, r.notify
	r.dirty, r.notify = map[string]map[string]bool{}, nil
	posted := map[string]bool{}
	for id := range r.posted {
		posted[id] = true
	}
	r.mu.Unlock()
	if len(dirty) == 0 && len(notify) == 0 {
		return nil
	}

	var chats []relayChat
	a.mu.Lock()
	for id, items := range dirty {
		c, ok := a.chats[id]
		// An empty New chat stays off the phone until its first message,
		// which goes up as its first post.
		if !ok || len(c.items) == 0 {
			continue
		}
		rc := relayChat{ID: id, Title: relayTitle(c), Dir: c.dir, Running: c.cancel != nil}
		for _, it := range c.items {
			if it.Tool != nil && it.Tool.Status == "approval" {
				rc.Waiting = true
			}
		}
		if !posted[id] {
			rc.Snapshot = true
			from := max(0, len(c.items)-relaySnapshot)
			for _, it := range c.items[from:] {
				rc.Items = append(rc.Items, relayItem(it))
			}
		} else {
			for item := range items {
				if it := c.find(item); it != nil {
					rc.Items = append(rc.Items, relayItem(*it))
				} else {
					rc.Removed = append(rc.Removed, item)
				}
			}
		}
		chats = append(chats, rc)
	}
	for i, n := range notify {
		c, ok := a.chats[n.Chat]
		if !ok {
			continue
		}
		notify[i].Title = relayTitle(c)
		if n.Kind == "done" && n.Body == "" {
			notify[i].Body = "Finished."
			for j := len(c.items) - 1; j >= 0; j-- {
				if it := c.items[j]; it.Kind == "assistant" && strings.TrimSpace(it.Text) != "" {
					notify[i].Body = clip(strings.Join(strings.Fields(it.Text), " "), 140)
					break
				}
			}
		}
	}
	a.mu.Unlock()
	if len(chats) == 0 && len(notify) == 0 {
		return nil
	}

	// The site takes 20 chats a post; the rest go with the next.
	var later []relayChat
	if len(chats) > 20 {
		chats, later = chats[:20], chats[20:]
	}
	err := relayCall(ctx, http.MethodPost, auth, "/api/relay/publish", map[string]any{"chats": chats, "notify": notify}, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		for id, items := range dirty {
			if r.dirty[id] == nil {
				r.dirty[id] = map[string]bool{}
			}
			for item := range items {
				r.dirty[id][item] = true
			}
		}
		r.notify = append(notify, r.notify...)
		return err
	}
	for _, c := range chats {
		r.posted[c.ID] = true
	}
	for _, c := range later {
		r.dirty[c.ID] = dirty[c.ID]
	}
	if len(later) > 0 {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// relayTitle is the chat's name, or before it has one its first message's
// first line, as the sidebar shows it.
func relayTitle(c *chat) string {
	if c.title != "" {
		return c.title
	}
	for _, it := range c.items {
		if it.Kind == "user" {
			line, _, _ := strings.Cut(strings.TrimSpace(it.Text), "\n")
			return clip(line, 80)
		}
	}
	return "New chat"
}

// relayItem is an item as the phone gets it: long output and diffs cut, as
// the phone shows the start of them.
func relayItem(it Item) Item {
	it = copyItem(it)
	it.Text = clip(it.Text, 32<<10)
	it.Reasoning = clip(it.Reasoning, 8<<10)
	if it.Tool != nil {
		it.Tool.Output = clip(it.Tool.Output, 4<<10)
		it.Tool.Diff = clip(it.Tool.Diff, 8<<10)
		it.Tool.Proposed = clip(it.Tool.Proposed, 8<<10)
	}
	return it
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut-- // not inside a UTF-8 character
	}
	return s[:cut] + "…"
}

// commandLoop asks the site for what the phone sent and does it.
func (a *App) commandLoop(ctx context.Context, r *relay) {
	wait := time.Duration(0)
	for ctx.Err() == nil {
		auth := a.relayAuth()
		if auth.Token == "" {
			wait = 5 * time.Second
		} else {
			var out struct {
				Commands []relayCommand `json:"commands"`
			}
			asked := time.Now()
			err := relayCall(ctx, http.MethodGet, auth, fmt.Sprintf("/api/relay/commands?wait=%d", relayWait), nil, &out)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("relay: commands: %v", err)
				wait = backoff(wait)
			} else {
				wait = 0
				if len(out.Commands) == 0 && time.Since(asked) < time.Second {
					wait = time.Second // a site that doesn't hold the request open
				}
				for _, c := range out.Commands {
					a.relayDo(c)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// relayDo does one thing the phone asked for.
func (a *App) relayDo(c relayCommand) {
	switch c.Kind {
	case "allow", "always", "deny":
		a.Answer(c.Chat, c.Call, c.Kind)
	case "stop":
		a.Stop(c.Chat)
	case "message":
		a.mu.Lock()
		_, open := a.chats[c.Chat]
		a.mu.Unlock()
		if !open {
			if _, err := a.OpenChat(c.Chat); err != nil {
				log.Printf("relay: opening %s for a message: %v", c.Chat, err)
				return
			}
		}
		// The window shows the chat's new turn only once it is opened there.
		if _, err := a.Send(c.Chat, c.Text); err != nil {
			log.Printf("relay: message to %s: %v", c.Chat, err)
		}
	}
}

func backoff(d time.Duration) time.Duration {
	if d < 2*time.Second {
		return 2 * time.Second
	}
	return min(d*2, time.Minute)
}

// relayCall makes one request to the site's relay as the account.
func relayCall(ctx context.Context, method string, auth cloud.Auth, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(auth.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: (relayWait + 15) * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s %s: %s %s", method, path, res.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
