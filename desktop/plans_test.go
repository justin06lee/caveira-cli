package main

import (
	"testing"

	"github.com/justin06lee/caveira/core/cloud"
)

// Free runs qwen-3.5, not the large ones; moving to one of them
// holds the message under the plans. Picking a plan opens its checkout on
// the site, and once the site says the account is on it, what was held
// goes without the window being touched.
func TestFreeHoldsTheLargeModels(t *testing.T) {
	watchQuickly(t)
	a, dir, st := appOn(t, "", scripted(sse("Big hello.", "", "", "")))
	opened := make(chan string, 4)
	a.browse = func(url string) error { opened <- url; return nil }

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetModel(v.ID, "glm-5.3", 0, ""); err != nil {
		t.Fatal(err)
	}
	if started, err := a.Send(v.ID, "hi"); err != nil || started {
		t.Fatalf("large model on free: started %v, %v", started, err)
	}
	v, _ = a.OpenChat(v.ID)
	if got := kinds(v.Items); got != "user paywall" || v.Items[1].Text != "GLM-5.3" {
		t.Fatalf("held: %q, %+v", got, v.Items)
	}

	if err := a.Subscribe("nope"); err == nil {
		t.Fatal("subscribed to a plan that is not there")
	}
	if err := a.Subscribe("lightweight"); err != nil {
		t.Fatal(err)
	}
	if url := <-opened; url != st.URL+"/pricing?plan=lightweight" {
		t.Fatalf("opened %q", url)
	}
	// Still on Free until the site says otherwise.
	a.Account()
	if v, _ = a.OpenChat(v.ID); kinds(v.Items) != "user paywall" {
		t.Fatalf("sent before paying: %q", kinds(v.Items))
	}

	st.set(func(s *site) { s.plan = "lightweight" })
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running && kinds(v.Items) != "user paywall" })
	if got := kinds(v.Items); got != "user assistant" || v.Items[1].Text != "Big hello." {
		t.Fatalf("after Lightweight: %q", got)
	}
	if v := a.Account(); v.Plan != "lightweight" || v.PlanName != "Lightweight" {
		t.Fatalf("account %+v", v)
	}
}

// The model picker is the catalog on the account,
// which serves the same models.
func TestModelsAreTheCatalogOnTheAccount(t *testing.T) {
	a, _ := testApp(t, nil)
	for _, signedIn := range []bool{true, false} {
		if !signedIn {
			if _, err := a.SignOut(); err != nil {
				t.Fatal(err)
			}
		}
		models, err := a.Models()
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, m := range models {
			names = append(names, m.Name+"|"+m.Plan)
		}
		want := []string{"Qwen3.5|", "GLM-5.2|Lightweight", "GLM-5.3|Lightweight"}
		if len(names) != len(want) {
			t.Fatalf("signed in %v: models %q", signedIn, names)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Fatalf("signed in %v: models %q, want %q", signedIn, names, want)
			}
		}
	}
}

// Switching a held chat to a model its plan runs sends what it held.
func TestSwitchingModelsSendsWhatWasHeld(t *testing.T) {
	a, dir, _ := appOn(t, "", scripted(sse("Small hello.", "", "", "")))

	v, err := a.NewChat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetModel(v.ID, "glm-5.2", 0, ""); err != nil {
		t.Fatal(err)
	}
	if started, _ := a.Send(v.ID, "hi"); started {
		t.Fatal("GLM-5.2 ran on Free")
	}
	if _, err := a.SetModel(v.ID, "qwen-3.5", 0, ""); err != nil {
		t.Fatal(err)
	}
	v = waitIdle(t, a, v.ID, func(v ChatView) bool { return !v.Running && len(v.Items) == 2 })
	if got := kinds(v.Items); got != "user assistant" || v.Items[1].Text != "Small hello." {
		t.Fatalf("after switching: %q", got)
	}
}

// Signed out, nothing runs but a local server; before the site has said
// what plan the account is on, the site decides.
func TestAllows(t *testing.T) {
	free := account{auth: cloud.Auth{Token: "t"}, known: true}
	free.me.Plan.ID = "free"
	paid := free
	paid.me.Plan.Large = true
	for _, c := range []struct {
		name   string
		ac     account
		model  string
		local  bool
		ok     bool
		locked string
	}{
		{"signed out", account{}, "qwen-3.5", false, false, ""},
		{"expired", account{auth: cloud.Auth{Token: "t"}, expired: true}, "qwen-3.5", false, false, ""},
		{"free, small", free, "qwen-3.5", false, true, ""},
		{"free, large", free, "glm-5.2", false, false, "GLM-5.2"},
		{"paid, large", paid, "glm-5.3", false, true, ""},
		{"signed out, local", account{}, "qwen3:8b", true, true, ""},
		{"free, local large", free, "glm-5.2", true, true, ""},
		{"not asked yet", account{auth: cloud.Auth{Token: "t"}}, "glm-5.2", false, true, ""},
	} {
		if ok, locked := allows(c.ac, c.model, c.local); ok != c.ok || locked != c.locked {
			t.Errorf("%s: %v %q, want %v %q", c.name, ok, locked, c.ok, c.locked)
		}
	}
}

// The plans state their allowance relative to Free, as the caveira site
// does, never in dollars.
func TestPlansStateTheirAllowance(t *testing.T) {
	want := map[string]string{
		"free": "Light usage", "lightweight": "10x the usage of Free", "middleweight": "25x the usage of Free",
		"heavyweight": "50x the usage of Free", "champion": "100x the usage of Free",
	}
	for _, p := range plans {
		line := p.Features[len(p.Features)-1]
		if line != want[p.ID] {
			t.Errorf("%s: %q", p.ID, line)
		}
	}
	if len(plans) != len(want) {
		t.Errorf("%d plans", len(plans))
	}
}
