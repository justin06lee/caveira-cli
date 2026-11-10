package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justin06lee/caveira/core/mcp"
)

// Settings searches the registry, adds a server with what it needs, lists
// it, turns it off and removes it; chats get its tools from the shared
// project manager.
func TestMCPFromSettings(t *testing.T) {
	a, dir := testApp(t, nil)
	t.Cleanup(a.stopAll)
	fixture, err := os.ReadFile("../core/mcp/testdata/registry.json")
	if err != nil {
		t.Fatal(err)
	}
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) }))
	defer reg.Close()
	old := mcp.RegistryURL
	mcp.RegistryURL = reg.URL
	t.Cleanup(func() { mcp.RegistryURL = old })

	found, err := a.SearchMCP("docker")
	if err != nil || len(found) != 6 {
		t.Fatalf("search: %d, %v", len(found), err)
	}
	var wts RegistryServer
	for _, f := range found {
		if f.Name == "io.github.artemdruzhininad-star/docker-wts-build" {
			wts = f
		}
	}
	if len(wts.Options) != 1 || len(wts.Options[0].Needs) != 1 || wts.Options[0].Needs[0].Name != "DWTB_PROJECT_DIR" {
		t.Fatalf("listing %+v", wts)
	}
	if _, err := a.AddMCP(dir, wts.Name, 0, nil); err == nil {
		t.Fatal("added without the value it needs")
	}
	name, err := a.AddMCP(dir, wts.Name, 0, map[string]string{"DWTB_PROJECT_DIR": "/p"})
	if err != nil || name != "docker-wts-build" {
		t.Fatalf("add: %q %v", name, err)
	}
	f, _ := mcp.ReadFile(mcp.UserPath())
	if s := f.Servers[name]; s.Command != "uvx" || s.Env["DWTB_PROJECT_DIR"] != "/p" {
		t.Fatalf("saved %+v", s)
	}

	states, err := a.SetMCPEnabled(dir, name, false)
	if err != nil || len(states) != 1 || states[0].Status != mcp.Off {
		t.Fatalf("off: %+v %v", states, err)
	}
	if states, err := a.RemoveMCP(dir, name); err != nil || len(states) != 0 {
		t.Fatalf("remove: %+v %v", states, err)
	}
}

func TestImportMCPFromSettings(t *testing.T) {
	a, dir := testApp(t, nil)
	t.Cleanup(a.stopAll)
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".cursor"), 0o755)
	os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"a":{"command":"/nonexistent/a"},"b":{"command":"/nonexistent/b"}}}`), 0o600)
	found := a.DetectMCP(dir)
	if len(found) != 2 {
		t.Fatalf("found %+v", found)
	}
	states, err := a.ImportMCP(dir, []int{1})
	if err != nil || len(states) != 1 || states[0].Name != "b" || states[0].Status != mcp.Failed {
		t.Fatalf("import %+v %v", states, err)
	}
}

// Turning a skill off rebuilds chats from the settings before their next
// turn, without it in the prompt.
func TestSkillsFromSettings(t *testing.T) {
	a, dir := testApp(t, nil)
	t.Cleanup(a.stopAll)
	home := os.Getenv("HOME")
	sk := filepath.Join(home, ".claude", "skills", "pdf")
	os.MkdirAll(sk, 0o755)
	os.WriteFile(filepath.Join(sk, "SKILL.md"), []byte("---\nname: pdf\ndescription: Work with PDFs.\n---\nbody"), 0o644)

	ag, gen, _ := a.build(dir)
	if !strings.Contains(ag.System, "- pdf: Work with PDFs.") {
		t.Fatal("skill not in a chat's prompt")
	}
	if _, ok := ag.Tools.Get("skill"); !ok || ag.Prepare == nil {
		t.Fatal("chat has no skill tool or no MCP servers")
	}
	list, err := a.SetSkillEnabled(dir, "pdf", false)
	if err != nil || len(list) != 1 || list[0].Enabled {
		t.Fatalf("off: %+v %v", list, err)
	}
	ag, gen2, _ := a.build(dir)
	if gen2 == gen || strings.Contains(ag.System, "pdf") {
		t.Fatal("chats keep the skill after it was turned off")
	}
	if _, err := a.RemoveSkill(dir, "pdf"); err == nil {
		t.Fatal("removed a skill caveira did not install")
	}
	if toolLabel("mcp__github__create_issue") != "MCP" || toolLabel("skill") != "Skill" {
		t.Fatal("labels")
	}
}
