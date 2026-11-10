package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/justin06lee/caveira/core/config"
)

// A Default is a server caveira sets up for you, once. It goes into
// ~/.caveira/mcp.json the first time caveira finds the program it runs
// with, and from then on it is an ordinary entry: turn it off or remove
// it like any other and it stays that way.
type Default struct {
	Name   string
	Server Server
}

// Defaults are the servers every caveira starts with.
var Defaults = []Default{{
	// REA (github.com/morluto/rea): reverse engineering of native
	// binaries, JavaScript/Electron apps, .NET assemblies and websites.
	// Pinned, so a new npm release can't change what runs without a
	// caveira release.
	Name:   "rea",
	Server: Server{Command: "npx", Args: []string{"-y", "rea-agents@6.7.0", "mcp"}},
}}

// seededPath records which defaults have been offered, so one that was
// removed is not put back.
func seededPath() string { return filepath.Join(config.Dir(), "mcp-defaults.json") }

func readSeeded() []string {
	var names []string
	if b, err := os.ReadFile(seededPath()); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	return names
}

// SeedDefaults adds to the user's mcp.json each default not yet offered
// whose program is installed. A default whose program is missing waits
// for a later start. An entry the user already has under that name is
// left alone.
func SeedDefaults() error {
	seeded := readSeeded()
	var todo []Default
	for _, d := range Defaults {
		if !slices.Contains(seeded, d.Name) {
			todo = append(todo, d)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	f, err := ReadFile(UserPath())
	if err != nil {
		return err // a broken mcp.json: leave it for the user to fix
	}
	changed := false
	for _, d := range todo {
		if _, has := f.Servers[d.Name]; !has {
			if d.Server.Transport() == "stdio" {
				if _, err := exec.LookPath(d.Server.Command); err != nil {
					continue
				}
			}
			f.Servers[d.Name] = d.Server
			changed = true
		}
		seeded = append(seeded, d.Name)
	}
	if changed {
		if err := WriteFile(UserPath(), f); err != nil {
			return err
		}
	}
	b, err := json.Marshal(seeded)
	if err != nil {
		return err
	}
	return errors.Join(os.MkdirAll(config.Dir(), 0o700), os.WriteFile(seededPath(), append(b, '\n'), 0o600))
}
