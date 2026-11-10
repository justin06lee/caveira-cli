package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// listings reads testdata/registry.json, recorded from the MCP Registry
// (registry.modelcontextprotocol.io/v0/servers?version=latest).
func listings(t *testing.T) map[string]Listing {
	t.Helper()
	f, err := os.Open("testdata/registry.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	list, err := parseListings(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Listing{}
	for _, l := range list {
		out[l.Name] = l
	}
	return out
}

func TestRegistryNpmWithNamedArgument(t *testing.T) {
	l := listings(t)["io.github.bytedance/mcp-server-filesystem"]
	opts := l.Options()
	// Of three npm packages only the stdio one runs as caveira runs servers.
	if len(opts) != 1 || opts[0].Label != "npm · @agent-infra/mcp-server-filesystem" {
		t.Fatalf("options: %+v", opts)
	}
	needs := opts[0].Needs()
	if len(needs) != 1 || needs[0].Name != "allowed-directories" {
		t.Fatalf("needs: %+v", needs)
	}
	if _, err := opts[0].Config(nil); err == nil {
		t.Fatal("configured without the required argument")
	}
	s, err := opts[0].Config(map[string]string{"allowed-directories": "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	want := Server{Command: "npx", Args: []string{"-y", "@agent-infra/mcp-server-filesystem", "--allowed-directories", "/tmp"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("server %+v", s)
	}
	if l.ShortName() != "mcp-server-filesystem" {
		t.Fatal(l.ShortName())
	}
}

func TestRegistryPypiWithEnv(t *testing.T) {
	l := listings(t)["io.github.artemdruzhininad-star/docker-wts-build"]
	o := l.Options()[0]
	if needs := o.Needs(); len(needs) != 1 || needs[0].Name != "DWTB_PROJECT_DIR" {
		t.Fatalf("needs %+v", needs)
	}
	s, err := o.Config(map[string]string{"DWTB_PROJECT_DIR": "/p", "DWTB_LOGS_DIR": "/l"})
	if err != nil {
		t.Fatal(err)
	}
	want := Server{Command: "uvx", Args: []string{"docker-wts-build@0.1.2"}, Env: map[string]string{"DWTB_PROJECT_DIR": "/p", "DWTB_LOGS_DIR": "/l"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("server %+v", s)
	}
}

func TestRegistryOCIAndMCPB(t *testing.T) {
	l := listings(t)["io.github.GavinLucas/docker-mcp-server"]
	opts := l.Options()
	// pypi and oci; the .mcpb bundle is not something caveira runs.
	if len(opts) != 2 || opts[0].Package.RegistryType != "pypi" || opts[1].Package.RegistryType != "oci" {
		t.Fatalf("options %+v", opts)
	}
	s, err := opts[1].Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Server{Command: "docker", Args: []string{"run", "-i", "--rm", "ghcr.io/gavinlucas/docker-mcp-server:2.1.1"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("server %+v", s)
	}
}

func TestRegistryNpmOptionalEnvLeftOut(t *testing.T) {
	o := listings(t)["io.github.hypnosis/docker-mcp-server"].Options()[0]
	if len(o.Needs()) != 0 {
		t.Fatalf("needs %+v", o.Needs())
	}
	s, _ := o.Config(map[string]string{"LOG_LEVEL": "debug"})
	want := Server{Command: "npx", Args: []string{"-y", "@hypnosis/docker-mcp-server@2.0.1"}, Env: map[string]string{"LOG_LEVEL": "debug"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("server %+v", s)
	}
}

func TestRegistryRemoteWithHeaderTemplate(t *testing.T) {
	o := listings(t)["ai.smithery/smithery-ai-github"].Options()[0]
	if o.Remote == nil || o.Label != "remote · https://server.smithery.ai/@smithery-ai/github/mcp" {
		t.Fatalf("option %+v", o)
	}
	needs := o.Needs()
	if len(needs) != 1 || needs[0].Name != "Authorization" || !needs[0].IsSecret {
		t.Fatalf("needs %+v", needs)
	}
	s, err := o.Config(map[string]string{"Authorization": "sk-123"})
	if err != nil {
		t.Fatal(err)
	}
	want := Server{Type: "http", URL: "https://server.smithery.ai/@smithery-ai/github/mcp", Headers: map[string]string{"Authorization": "Bearer sk-123"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("server %+v", s)
	}
}

func TestRegistrySSERemoteOptionalHeader(t *testing.T) {
	o := listings(t)["io.github.Evozim/dockerfile-credential-mcp"].Options()[0]
	s, err := o.Config(nil)
	if err != nil || s.Type != "sse" || s.Headers != nil {
		t.Fatalf("server %+v %v", s, err)
	}
}

func TestSearchAsksForLatest(t *testing.T) {
	b, _ := os.ReadFile("testdata/registry.json")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		w.Write(b)
	}))
	defer srv.Close()
	old := RegistryURL
	RegistryURL = srv.URL
	defer func() { RegistryURL = old }()
	list, err := Search(context.Background(), "docker files")
	if err != nil || len(list) != 6 {
		t.Fatalf("%d listings, %v", len(list), err)
	}
	if got != "limit=30&search=docker+files&version=latest" {
		t.Fatalf("query %q", got)
	}
}

// Servers set up for Claude Code, Claude Desktop and Cursor are found,
// and copied in only when picked.
func TestDetectAndImport(t *testing.T) {
	home(t)
	h := os.Getenv("HOME")
	work := project(t)
	root := filepath.Dir(work)
	writeJSON(t, filepath.Join(h, ".claude.json"), `{"numStartups": 3,
		"mcpServers": {"cc-user": {"type": "stdio", "command": "npx", "args": ["-y", "a"], "env": {"K": "v"}}},
		"projects": {`+jsonString(root)+`: {"mcpServers": {"cc-proj": {"type": "http", "url": "https://x.example/mcp"}}},
		             "/elsewhere": {"mcpServers": {"other": {"command": "x"}}}}}`)
	writeJSON(t, filepath.Join(root, ".mcp.json"), `{"mcpServers": {"shared": {"command": "uvx", "args": ["s"]}, "bad name!": {"command": "x"}}}`)
	desk := filepath.Join(h, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
		desk = filepath.Join(h, "AppData", "Roaming", "Claude", "claude_desktop_config.json")
	}
	writeJSON(t, desk, `{"mcpServers": {"desk": {"command": "node", "args": ["d.js"]}}}`)
	writeJSON(t, filepath.Join(h, ".cursor", "mcp.json"), `{"mcpServers": {"cur": {"url": "https://c.example/sse", "type": "sse"}, "broken": {}}}`)
	writeJSON(t, UserPath(), `{"mcpServers": {"desk": {"command": "old"}}}`)

	found := Detect(work)
	got := map[string]Found{}
	for _, f := range found {
		got[f.Name] = f
	}
	if len(found) != 5 {
		t.Fatalf("found %d: %+v", len(found), found)
	}
	if f := got["cc-user"]; f.Source != "Claude Code" || f.Scope != User || f.Server.Env["K"] != "v" {
		t.Fatalf("cc-user %+v", f)
	}
	if f := got["cc-proj"]; f.Source != "Claude Code (this project)" || f.Server.Transport() != "http" {
		t.Fatalf("cc-proj %+v", f)
	}
	if f := got["shared"]; f.Scope != Project {
		t.Fatalf("shared %+v", f)
	}
	if f := got["desk"]; f.Source != "Claude Desktop" || !f.Have {
		t.Fatalf("desk %+v", f)
	}
	if f := got["cur"]; f.Source != "Cursor" || f.Server.Transport() != "sse" {
		t.Fatalf("cur %+v", f)
	}

	// Nothing was copied by looking.
	if u, _ := ReadFile(UserPath()); len(u.Servers) != 1 {
		t.Fatalf("detect wrote %+v", u)
	}
	n, err := Import(work, []Found{got["cc-user"], got["shared"]})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	entries, _ := Load(work)
	names := map[string]Scope{}
	for _, e := range entries {
		names[e.Name] = e.Scope
	}
	if names["cc-user"] != User || names["shared"] != Project || names["desk"] != User || len(names) != 3 {
		t.Fatalf("after import %+v", names)
	}
}

// jsonString is s as a JSON string, backslashes of a Windows path escaped.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
