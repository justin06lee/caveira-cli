package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
)

// Plan is a caveira subscription. A chat runs on a caveira account, Free
// or paid, or on a local model server when one is set: signed out, a
// message is held in the chat behind a sign-in until there is one, and a
// model the plan lacks is held behind the plans.
//
// The plan is the account's, as the caveira site reports it (account.go);
// it is bought there, in the browser. Everything that asks whether a chat
// may run goes through allows.
type Plan struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int    `json:"price"` // US dollars a month
	// Features is what it comes with.
	Features []string `json:"features"`
	// Popular marks the plan the plans page points to.
	Popular bool `json:"popular,omitempty"`
	// Large says it runs the large models, not only qwen-3.5.
	Large bool `json:"large"`
}

// Each plan comes with a monthly allowance of usage, as the caveira site
// counts it (src/lib/plans.ts there). It is stated relative to Free's,
// as cloud.Allowance says it, never in dollars.
var plans = []Plan{
	{ID: "free", Name: "Free", Price: 0,
		Features: []string{"Qwen3.5", "256K token context", "Light usage"}},
	{ID: "lightweight", Name: "Lightweight", Price: 20, Popular: true, Large: true,
		Features: []string{"GLM-5.2 and GLM-5.3", "1M token context", "10x the usage of Free"}},
	{ID: "middleweight", Name: "Middleweight", Price: 50, Large: true,
		Features: []string{"Everything in Lightweight", "25x the usage of Free"}},
	{ID: "heavyweight", Name: "Heavyweight", Price: 100, Large: true,
		Features: []string{"Everything in Middleweight", "50x the usage of Free"}},
	{ID: "champion", Name: "Champion", Price: 200, Large: true,
		Features: []string{"Everything in Heavyweight", "100x the usage of Free"}},
}

func planByID(id string) (Plan, bool) {
	for _, p := range plans {
		if p.ID == id {
			return p, true
		}
	}
	return Plan{}, false
}

// catalogModel is a model caveira offers through the account.
type catalogModel struct {
	id, name string
	logos    []string
	large    bool
}

// catalog is the models the desktop app runs through the account, in the
// order the picker shows them, by the open-weight model each comes from:
// Qwen3.5, and GLM for the large ones. The ids are the names caveira's
// server lists and accepts.
var catalog = []catalogModel{
	{id: "qwen-3.5", name: "Qwen3.5", logos: []string{"qwen"}},
	{id: "glm-5.2", name: "GLM-5.2", logos: []string{"zai"}, large: true},
	{id: "glm-5.3", name: "GLM-5.3", logos: []string{"zai"}, large: true},
}

func catalogEntry(id string) (catalogModel, bool) {
	for _, m := range catalog {
		if m.id == id {
			return m, true
		}
	}
	return catalogModel{}, false
}

// largePlan is the cheapest plan with the large models, for "comes with".
func largePlan() Plan {
	for _, p := range plans {
		if p.Large {
			return p
		}
	}
	return plans[len(plans)-1]
}

// allows says whether the account can run model, and if not, why not:
// the name of the model its plan lacks, or "" when it is signed out.
// Before the site has said what plan it is on, the site decides when the
// message reaches it. A chat on a local model server needs neither.
func allows(ac account, model string, local bool) (ok bool, locked string) {
	if local {
		return true, ""
	}
	if !ac.signedIn() {
		return false, ""
	}
	if m, known := catalogEntry(model); known && m.large && ac.known && !ac.me.Plan.Large && !ac.me.Admin {
		return false, m.name
	}
	return true, ""
}

// Subscribe opens the plan's checkout on the caveira site in the browser,
// which signs in there first if need be. What chats were holding goes
// once the site says the account runs it (see watch).
func (a *App) Subscribe(plan string) error {
	if _, ok := planByID(plan); !ok {
		return fmt.Errorf("there is no %q plan", plan)
	}
	a.mu.Lock()
	site := a.siteLocked()
	a.mu.Unlock()
	a.watch()
	return a.browse(site + "/pricing?plan=" + url.QueryEscape(plan))
}

// hold keeps a message the chat cannot run yet, under a sign-in or the
// plans. A second one goes with the first. Caller holds App.mu.
func (c *chat) hold(text, locked string) []ChatEvent {
	var out []ChatEvent
	if id := c.paywall(); id != "" {
		c.remove(id)
		out = append(out, ChatEvent{Chat: c.id, Type: "remove", ID: id})
	}
	c.held = append(c.held, text)
	out = append(out, c.push(Item{Kind: "user", Text: text}), c.push(Item{Kind: "paywall", Text: locked}))
	return out
}

// paywall is the plans item in the transcript, "" if there is none.
func (c *chat) paywall() string {
	for _, it := range c.items {
		if it.Kind == "paywall" {
			return it.ID
		}
	}
	return ""
}

// release sends what a chat was holding once the account runs it: the
// messages already on screen go to the model as they are. A chat built
// before a sign-in is built again first, to send through the account.
func (a *App) release(id string) {
	a.mu.Lock()
	c, ok := a.chats[id]
	if !ok || len(c.held) == 0 || c.cancel != nil {
		a.mu.Unlock()
		return
	}
	// Signed out nothing runs, so a chat that cannot is left as it is.
	stale := c.gen != a.gen || c.problem != "" && a.acct.signedIn()
	a.mu.Unlock()
	if stale {
		a.rebuild(c)
	}

	a.mu.Lock()
	if len(c.held) == 0 || c.cancel != nil || c.problem != "" {
		a.mu.Unlock()
		return
	}
	model, _ := c.ag.ModelInfo()
	if ok, locked := allows(a.acct, model, c.local()); !ok {
		// Still held, maybe for another reason: signed in since, but on a
		// plan without the model.
		out := c.rewall(locked)
		a.mu.Unlock()
		for _, e := range out {
			a.emit("chat", e)
		}
		return
	}
	var out []ChatEvent
	if pid := c.paywall(); pid != "" {
		c.remove(pid)
		out = append(out, ChatEvent{Chat: c.id, Type: "remove", ID: pid})
	}
	texts := c.held
	c.held = nil
	ag := c.ag
	ctx := c.begin()
	a.mu.Unlock()

	for _, e := range out {
		a.emit("chat", e)
	}
	go func() {
		notes(ag, texts[:len(texts)-1])
		text := texts[len(texts)-1]
		a.turn(ctx, c, ag, func(ctx context.Context, emit func(agent.Event)) { ag.Run(ctx, text, emit) })
	}()
}

// releaseAll sends what every chat was holding that the account runs now.
func (a *App) releaseAll() {
	a.mu.Lock()
	var ids []string
	for id, c := range a.chats {
		if len(c.held) > 0 {
			ids = append(ids, id)
		}
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.release(id)
	}
}

// rewall gives the paywall the reason the chat is held for now. Caller
// holds App.mu.
func (c *chat) rewall(locked string) []ChatEvent {
	it := c.find(c.paywall())
	if it == nil || it.Text == locked {
		return nil
	}
	it.Text = locked
	return []ChatEvent{c.itemEvent(it)}
}

// notes puts messages sent while the chat was held ahead of the last one,
// the way the terminal client sends what was queued.
func notes(ag *agent.Agent, texts []string) {
	if len(texts) == 0 {
		return
	}
	if ag.Session == nil {
		ag.NewSession()
	}
	for _, t := range texts {
		ag.Note(t)
	}
}

// catalogOptions is the picker's list for the account.
func catalogOptions() []ModelOption {
	out := make([]ModelOption, 0, len(catalog))
	for _, m := range catalog {
		o := ModelOption{
			ID: m.id, Name: m.name, Logos: m.logos,
			Context: config.Spec(m.id).ContextWindow,
		}
		if m.large {
			o.Plan = largePlan().Name
		}
		out = append(out, o)
	}
	return out
}

// local says the chat sends to a local model server, not the account.
// Only the account's requests carry a token, and a caveira site run on
// this machine for development is still the account.
// A local server's own key is not the account's token.
func (c *chat) local() bool {
	if !config.IsLocal(c.ag.Client.BaseURL) {
		return false
	}
	if c.ag.Client.APIKey == "" {
		return true
	}
	a, ok := cloud.Load()
	return !ok || a.Token != c.ag.Client.APIKey
}

// isCatalog says requests go through the caveira account, whose models
// are the catalog's, or will once it is signed in: anything but a local
// model server.
func isCatalog(cfg config.Settings) bool { return !cfg.Local() }
