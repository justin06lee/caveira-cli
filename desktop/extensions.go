package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/mcp"
	"github.com/justin06lee/caveira/core/skills"
)

// MCP servers and skills, for Settings and for the chats. Servers run
// once per project, shared by its chats, and start with the first chat
// opened there; skills are read from disk each time a chat is built.

// servers is the project's MCP servers, started the first time they are
// asked for. Call with a.mu held.
func (a *App) serversLocked(dir string) *mcp.Manager {
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	root := mcp.ProjectRoot(dir)
	if a.mcp == nil {
		a.mcp = map[string]*mcp.Manager{}
	}
	m := a.mcp[root]
	if m == nil {
		_ = mcp.SeedDefaults() // REA and any other built-in servers, once
		m = mcp.NewManager(root, a.version)
		// Chats in a project can run different models; Settings shows
		// whether tools are deferred for the configured one.
		m.Window = func() int {
			cfg, _ := config.Load(root)
			if cfg.ContextWindow > 0 {
				return cfg.ContextWindow
			}
			return config.Spec(cfg.Model).ContextWindow
		}
		m.Start()
		a.mcp[root] = m
	}
	return m
}

func (a *App) servers(dir string) *mcp.Manager {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.serversLocked(dir)
}

// equip gives a chat's agent the project's MCP tools and its skills.
func (a *App) equip(ag *agent.Agent, dir string) {
	skills.Use(ag.Tools, dir)
	m := a.servers(dir)
	ag.Prepare = m.Prepare(ag.Tools)
}

// restartServers picks up a change to mcp.json in every running project.
// A server set up for you is in all of them.
func (a *App) restartServers() {
	a.mu.Lock()
	list := make([]*mcp.Manager, 0, len(a.mcp))
	for _, m := range a.mcp {
		list = append(list, m)
	}
	a.mu.Unlock()
	for _, m := range list {
		m.Start()
	}
}

// MCPServers is the servers set up for the project at dir (or just
// yours, with no project), and how each is. Ones still starting are
// waited for a moment.
func (a *App) MCPServers(dir string) []mcp.State {
	m := a.servers(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.Wait(ctx, 3*time.Second)
	return m.States()
}

// SetMCPEnabled turns a server on or off where it is set up.
func (a *App) SetMCPEnabled(dir, name string, on bool) ([]mcp.State, error) {
	if err := mcp.SetEnabled(dir, name, on); err != nil {
		return nil, err
	}
	a.restartServers()
	return a.MCPServers(dir), nil
}

// RemoveMCP takes a server out of the file it is set up in.
func (a *App) RemoveMCP(dir, name string) ([]mcp.State, error) {
	if err := mcp.Remove(dir, name); err != nil {
		return nil, err
	}
	a.restartServers()
	return a.MCPServers(dir), nil
}

// RegistryServer is a server in the MCP Registry, with the ways caveira
// can run it.
type RegistryServer struct {
	Name        string      `json:"name"`
	Short       string      `json:"short"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Options     []RegistryWay `json:"options"`
}

// RegistryWay is one way to run a server, and the values it needs.
type RegistryWay struct {
	Label string      `json:"label"`
	Needs []mcp.Input `json:"needs"`
}

// SearchMCP looks for servers in the MCP Registry.
func (a *App) SearchMCP(query string) ([]RegistryServer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	list, err := mcp.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.listings = map[string]mcp.Listing{}
	for _, l := range list {
		a.listings[l.Name] = l
	}
	a.mu.Unlock()
	out := make([]RegistryServer, 0, len(list))
	for _, l := range list {
		s := RegistryServer{Name: l.Name, Short: l.ShortName(), Title: l.Title, Description: l.Description}
		for _, o := range l.Options() {
			needs := o.Needs()
			if needs == nil {
				needs = []mcp.Input{}
			}
			s.Options = append(s.Options, RegistryWay{Label: o.Label, Needs: needs})
		}
		out = append(out, s)
	}
	return out, nil
}

// AddMCP adds a server from the last search to ~/.caveira/mcp.json, run
// the way at index way, with values for what it needs. It says the name
// it went in under.
func (a *App) AddMCP(dir, registryName string, way int, values map[string]string) (string, error) {
	a.mu.Lock()
	l, ok := a.listings[registryName]
	a.mu.Unlock()
	if !ok {
		return "", errors.New("search the registry again; " + registryName + " is not in the last results")
	}
	opts := l.Options()
	if way < 0 || way >= len(opts) {
		return "", errors.New("no such way to run " + registryName)
	}
	s, err := opts[way].Config(values)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, st := range a.servers(dir).States() {
		taken[st.Name] = true
	}
	name := l.ShortName()
	for i := 2; taken[name]; i++ {
		name = l.ShortName() + "-" + strconv.Itoa(i)
	}
	if err := mcp.Add(mcp.User, dir, name, s); err != nil {
		return "", err
	}
	a.restartServers()
	return name, nil
}

// DetectMCP is the servers set up in Claude Code, Claude Desktop and
// Cursor, offered to copy in. Nothing is copied until ImportMCP.
func (a *App) DetectMCP(dir string) []mcp.Found {
	found := mcp.Detect(dir)
	if found == nil {
		found = []mcp.Found{}
	}
	return found
}

// ImportMCP copies in the servers picked from DetectMCP's list, by index.
func (a *App) ImportMCP(dir string, picks []int) ([]mcp.State, error) {
	found := mcp.Detect(dir)
	var chosen []mcp.Found
	for _, i := range picks {
		if i >= 0 && i < len(found) {
			chosen = append(chosen, found[i])
		}
	}
	_, err := mcp.Import(dir, chosen)
	a.restartServers()
	return a.MCPServers(dir), err
}

// Skills is the skills for the project at dir, on and off.
func (a *App) Skills(dir string) []skills.Skill {
	list := skills.Find(dir)
	if list == nil {
		list = []skills.Skill{}
	}
	return list
}

// skillsChanged makes chats pick up the skills on now: each is built
// again before its next turn.
func (a *App) skillsChanged() {
	a.mu.Lock()
	a.gen++
	a.mu.Unlock()
}

// SetSkillEnabled turns a skill on or off everywhere.
func (a *App) SetSkillEnabled(dir, name string, on bool) ([]skills.Skill, error) {
	if err := skills.SetEnabled(name, on); err != nil {
		return nil, err
	}
	a.skillsChanged()
	return a.Skills(dir), nil
}

// RemoveSkill deletes a skill caveira installed.
func (a *App) RemoveSkill(dir, name string) ([]skills.Skill, error) {
	if err := skills.Remove(dir, name); err != nil {
		return nil, err
	}
	a.skillsChanged()
	return a.Skills(dir), nil
}

// InstallSkill installs the skills at a GitHub address into
// ~/.caveira/skills.
func (a *App) InstallSkill(dir, source string) ([]skills.Skill, error) {
	src, err := skills.ParseSource(source)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := skills.Install(ctx, src); err != nil {
		return nil, err
	}
	a.skillsChanged()
	return a.Skills(dir), nil
}

// updateRea lets the built-in rea server update itself, verified, in the
// background (core/mcp/reaupdate.go): a newer version starts from the next
// launch, and the chats running now keep the one they have.
func (a *App) updateRea(ctx context.Context) {
	waitShellEnv() // node and npm are on the login shell's PATH
	time.Sleep(3 * time.Second)
	msg, err := mcp.UpdateRea(ctx)
	if err != nil {
		log.Printf("rea: %v", err)
		return
	}
	log.Printf("rea: %s", msg)
}
