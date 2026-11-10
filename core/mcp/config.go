// Package mcp connects caveira to Model Context Protocol servers: it reads
// which servers are set up, starts them when a session starts, and offers
// their tools to the model as mcp__<server>__<tool>, next to the built-in
// ones. It also finds servers set up for other agents (Claude Code, Claude
// Desktop, Cursor) and turns entries of the official MCP Registry into
// configuration.
//
// Servers are set up in ~/.caveira/mcp.json, in the shape Claude Code
// uses:
//
//	{"mcpServers": {
//	  "files":  {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]},
//	  "remote": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer …"}}
//	}}
//
// A project's .caveira/mcp.json is laid over it, server by server.
// "disabled": true keeps a server set up without starting it.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/justin06lee/caveira/core/config"
)

// Server is one server's entry.
type Server struct {
	// Type is "stdio" (the default when there is a command), "http" for
	// streamable HTTP, or "sse" for the older HTTP with server-sent
	// events.
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Disabled keeps the entry without starting the server.
	Disabled bool `json:"disabled,omitempty"`
}

// Transport is how caveira talks to s: "stdio", "http" or "sse".
func (s Server) Transport() string {
	switch strings.ToLower(s.Type) {
	case "http", "streamable-http", "streamablehttp":
		return "http"
	case "sse":
		return "sse"
	case "stdio":
		return "stdio"
	}
	if s.Command == "" && s.URL != "" {
		return "http"
	}
	return "stdio"
}

// Check says what is wrong with s, or nil.
func (s Server) Check() error {
	switch s.Transport() {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return errors.New("no command to run")
		}
	default:
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return fmt.Errorf("%q is not an http(s) address", s.URL)
		}
	}
	return nil
}

// Summary is one line saying what s runs or where it is.
func (s Server) Summary() string {
	if s.Transport() == "stdio" {
		return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
	}
	return s.URL
}

// File is a mcp.json.
type File struct {
	Servers map[string]Server `json:"mcpServers"`
}

// Scope says which file an entry is in.
type Scope string

const (
	User    Scope = "user"    // ~/.caveira/mcp.json
	Project Scope = "project" // <project>/.caveira/mcp.json
)

// Entry is a configured server with its name and where it was set up.
type Entry struct {
	Name  string
	Scope Scope
	Server
}

// UserPath is ~/.caveira/mcp.json.
func UserPath() string { return filepath.Join(config.Dir(), "mcp.json") }

// ProjectRoot is the folder a project's .caveira lives in: the git root
// above workDir, or workDir itself outside a repository.
func ProjectRoot(workDir string) string {
	for d := workDir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return workDir
		}
	}
}

// ProjectPath is the project's .caveira/mcp.json.
func ProjectPath(workDir string) string {
	return filepath.Join(ProjectRoot(workDir), ".caveira", "mcp.json")
}

// ReadFile reads a mcp.json; a missing one is empty.
func ReadFile(path string) (File, error) {
	f := File{Servers: map[string]Server{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{Servers: map[string]Server{}}, fmt.Errorf("%s: %w", path, err)
	}
	if f.Servers == nil {
		f.Servers = map[string]Server{}
	}
	return f, nil
}

// WriteFile writes a mcp.json, readable only by you: entries can hold
// tokens.
func WriteFile(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load is the servers for workDir: the user's, with the project's laid
// over them by name, sorted by name.
func Load(workDir string) ([]Entry, error) {
	var errs []error
	merged := map[string]Entry{}
	for _, src := range []struct {
		path  string
		scope Scope
	}{{UserPath(), User}, {ProjectPath(workDir), Project}} {
		f, err := ReadFile(src.path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for name, s := range f.Servers {
			merged[name] = Entry{Name: name, Scope: src.scope, Server: s}
		}
	}
	out := make([]Entry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errors.Join(errs...)
}

func scopePath(scope Scope, workDir string) string {
	if scope == Project {
		return ProjectPath(workDir)
	}
	return UserPath()
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,40}$`)

// ValidName says name can name a server: letters, digits, _ . -, up to 41.
func ValidName(name string) bool { return nameRE.MatchString(name) && !strings.Contains(name, "__") }

// Add puts s in scope's file under name, replacing one of that name.
func Add(scope Scope, workDir, name string, s Server) error {
	if !ValidName(name) {
		return fmt.Errorf("%q cannot name a server: use letters, digits, - and _", name)
	}
	if err := s.Check(); err != nil {
		return err
	}
	path := scopePath(scope, workDir)
	f, err := ReadFile(path)
	if err != nil {
		return err
	}
	f.Servers[name] = s
	return WriteFile(path, f)
}

// Remove takes the server name out of the file it was set up in.
func Remove(workDir, name string) error {
	return change(workDir, name, func(f *File) { delete(f.Servers, name) })
}

// SetEnabled turns the server name on or off where it was set up.
func SetEnabled(workDir, name string, on bool) error {
	return change(workDir, name, func(f *File) {
		s := f.Servers[name]
		s.Disabled = !on
		f.Servers[name] = s
	})
}

// change edits the nearest file that has name: the project's, then the
// user's.
func change(workDir, name string, edit func(*File)) error {
	for _, path := range []string{ProjectPath(workDir), UserPath()} {
		f, err := ReadFile(path)
		if err != nil {
			return err
		}
		if _, ok := f.Servers[name]; ok {
			edit(&f)
			return WriteFile(path, f)
		}
	}
	return fmt.Errorf("no MCP server called %s", name)
}

var varRE = regexp.MustCompile(`\$\{([^}]+)\}`)

// expand fills ${VAR} and ${VAR:-default} from extra and the
// environment, as Claude Code's configuration does. A bare $VAR is left
// alone.
func expand(s string, extra map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return varRE.ReplaceAllStringFunc(s, func(m string) string {
		key, def := m[2:len(m)-1], ""
		if k, d, ok := strings.Cut(key, ":-"); ok {
			key, def = k, d
		}
		if v := extra[key]; v != "" {
			return v
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	})
}
