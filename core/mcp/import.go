package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Found is a server set up for another agent, offered to copy in.
type Found struct {
	// Source is the app it was found for: Claude Code, Claude Desktop,
	// Cursor.
	Source string `json:"source"`
	Path   string `json:"path"`
	Name   string `json:"name"`
	Server Server `json:"server"`
	// Scope is where it goes when copied: the project for a project's
	// .mcp.json, your own file for the rest.
	Scope Scope `json:"scope"`
	// Have says a server of that name is set up already; copying it
	// replaces that one.
	Have bool `json:"have"`
}

// Detect looks for servers set up in Claude Code (~/.claude.json, for
// you and for this project, and the project's .mcp.json), Claude Desktop
// and Cursor. Nothing is copied; Import does that for what is picked.
func Detect(workDir string) []Found {
	home, _ := os.UserHomeDir()
	root := ProjectRoot(workDir)
	have := map[string]bool{}
	if entries, err := Load(workDir); err == nil {
		for _, e := range entries {
			have[e.Name] = true
		}
	}

	var out []Found
	add := func(source, path string, scope Scope, servers map[string]Server) {
		for _, name := range sortedKeys(servers) {
			s := servers[name]
			if !ValidName(name) || s.Check() != nil {
				continue
			}
			out = append(out, Found{Source: source, Path: path, Name: name, Server: s, Scope: scope, Have: have[name]})
		}
	}

	claude := filepath.Join(home, ".claude.json")
	var cc struct {
		Servers  map[string]Server `json:"mcpServers"`
		Projects map[string]struct {
			Servers map[string]Server `json:"mcpServers"`
		} `json:"projects"`
	}
	if readJSON(claude, &cc) {
		add("Claude Code", claude, User, cc.Servers)
		// Claude Code keys its projects by path, with forward slashes on Windows.
		for _, dir := range []string{workDir, filepath.ToSlash(workDir), root, filepath.ToSlash(root)} {
			if p, ok := cc.Projects[dir]; ok {
				add("Claude Code (this project)", claude, User, p.Servers)
				break
			}
		}
	}
	var f File
	if p := filepath.Join(root, ".mcp.json"); readJSON(p, &f) {
		add("Claude Code (.mcp.json)", p, Project, f.Servers)
	}
	desktop := filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	if appdata := os.Getenv("APPDATA"); appdata != "" && runtime.GOOS == "windows" {
		desktop = filepath.Join(appdata, "Claude", "claude_desktop_config.json")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && !fileExists(desktop) {
		desktop = filepath.Join(xdg, "Claude", "claude_desktop_config.json")
	}
	f = File{}
	if readJSON(desktop, &f) {
		add("Claude Desktop", desktop, User, f.Servers)
	}
	cursor := filepath.Join(home, ".cursor", "mcp.json")
	f = File{}
	if readJSON(cursor, &f) {
		add("Cursor", cursor, User, f.Servers)
	}
	return out
}

// Import copies the picked servers in, each to its Scope's file, and
// says how many went in.
func Import(workDir string, picks []Found) (int, error) {
	n := 0
	for _, p := range picks {
		if err := Add(p.Scope, workDir, p.Name, p.Server); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func readJSON(path string, into any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, into) == nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
