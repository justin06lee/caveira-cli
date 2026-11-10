package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/justin06lee/caveira/core/config"
)

// The servers whose calls run without asking, per project, from "always
// allow" in an approval: ~/.caveira/mcp-allow.json, project root →
// server names. It lives outside the project so a repository cannot allow
// its own servers.

var allowMu sync.Mutex

func allowPath() string { return filepath.Join(config.Dir(), "mcp-allow.json") }

func readAllow() map[string][]string {
	out := map[string][]string{}
	if b, err := os.ReadFile(allowPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// Allowed says the server's calls in the project at workDir run without
// asking.
func Allowed(workDir, server string) bool {
	allowMu.Lock()
	defer allowMu.Unlock()
	return slices.Contains(readAllow()[ProjectRoot(workDir)], server)
}

// Allow stops asking about the server's calls in the project at workDir.
func Allow(workDir, server string) error {
	allowMu.Lock()
	defer allowMu.Unlock()
	all := readAllow()
	root := ProjectRoot(workDir)
	if slices.Contains(all[root], server) {
		return nil
	}
	all[root] = append(all[root], server)
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(allowPath(), append(b, '\n'), 0o600)
}
