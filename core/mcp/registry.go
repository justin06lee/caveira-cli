package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RegistryURL is the official MCP Registry's server list.
var RegistryURL = "https://registry.modelcontextprotocol.io/v0/servers"

// Listing is one server in the registry, latest version.
type Listing struct {
	Name        string               `json:"name"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description"`
	Version     string               `json:"version"`
	Repository  struct{ URL string } `json:"repository"`
	Packages    []Package            `json:"packages,omitempty"`
	Remotes     []Remote             `json:"remotes,omitempty"`
}

// Package is a way to run a server locally.
type Package struct {
	RegistryType string `json:"registryType"`
	Identifier   string `json:"identifier"`
	Version      string `json:"version,omitempty"`
	RuntimeHint  string `json:"runtimeHint,omitempty"`
	Transport    struct {
		Type string `json:"type"`
	} `json:"transport"`
	RuntimeArguments     []Argument `json:"runtimeArguments,omitempty"`
	PackageArguments     []Argument `json:"packageArguments,omitempty"`
	EnvironmentVariables []Input    `json:"environmentVariables,omitempty"`
}

// Remote is a server someone runs, reached over HTTP.
type Remote struct {
	Type    string  `json:"type"`
	URL     string  `json:"url"`
	Headers []Input `json:"headers,omitempty"`
}

// Input is a value a server needs: an environment variable or a header.
type Input struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
	Default     string `json:"default,omitempty"`
	// Value is a fixed value or a template like "Bearer {api_key}".
	Value string `json:"value,omitempty"`
}

// Argument is a command-line argument of a package.
type Argument struct {
	Input
	Type      string `json:"type"` // positional or named
	ValueHint string `json:"valueHint,omitempty"`
}

// Search asks the registry for servers matching query, latest versions
// only.
func Search(ctx context.Context, query string) ([]Listing, error) {
	q := url.Values{"search": {strings.TrimSpace(query)}, "version": {"latest"}, "limit": {"30"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RegistryURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "caveira")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the MCP Registry answered %s", resp.Status)
	}
	return parseListings(io.LimitReader(resp.Body, 8<<20))
}

func parseListings(r io.Reader) ([]Listing, error) {
	var body struct {
		Servers []struct {
			Server Listing `json:"server"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading the MCP Registry: %w", err)
	}
	out := make([]Listing, 0, len(body.Servers))
	for _, s := range body.Servers {
		if len(s.Server.Options()) > 0 {
			out = append(out, s.Server)
		}
	}
	return out, nil
}

// ShortName is what the server is called once added: the last part of
// its registry name, made a valid server name.
func (l Listing) ShortName() string {
	n := l.Name
	if i := strings.LastIndexAny(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	n = regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(n, "-")
	n = strings.Trim(strings.ReplaceAll(n, "__", "_"), "-_.")
	if len(n) > 40 {
		n = n[:40]
	}
	if n == "" {
		n = "server"
	}
	return n
}

// Option is one way to install a listing: a package caveira can run, or
// a remote.
type Option struct {
	Label   string
	Package *Package
	Remote  *Remote
}

// runner is the command each package type runs with.
var runner = map[string]string{"npm": "npx", "pypi": "uvx", "oci": "docker"}

// Options are the ways caveira can install l, best first: a package it
// runs over stdio, then a remote.
func (l Listing) Options() []Option {
	var out []Option
	for i := range l.Packages {
		p := &l.Packages[i]
		if runner[p.RegistryType] == "" || (p.Transport.Type != "" && p.Transport.Type != "stdio") {
			continue
		}
		label := p.RegistryType + " · " + p.Identifier
		if p.RegistryType != "oci" && p.Version != "" && p.Version != "latest" {
			label += "@" + p.Version
		}
		out = append(out, Option{Label: label, Package: p})
	}
	for i := range l.Remotes {
		r := &l.Remotes[i]
		if r.Type != "streamable-http" && r.Type != "sse" {
			continue
		}
		out = append(out, Option{Label: "remote · " + r.URL, Remote: r})
	}
	return out
}

// Needs is what has to be asked for before o can run: required values
// without a default or a fixed value.
func (o Option) Needs() []Input {
	var out []Input
	need := func(in Input) {
		if !in.IsRequired || in.Default != "" {
			return
		}
		if in.Value != "" && !placeholderRE.MatchString(in.Value) {
			return
		}
		out = append(out, in)
	}
	if p := o.Package; p != nil {
		for _, e := range p.EnvironmentVariables {
			need(e)
		}
		for _, a := range append(append([]Argument{}, p.RuntimeArguments...), p.PackageArguments...) {
			in := a.Input
			if in.Name == "" {
				in.Name = a.ValueHint
			}
			need(in)
		}
	}
	if r := o.Remote; r != nil {
		for _, h := range r.Headers {
			need(h)
		}
	}
	return out
}

var placeholderRE = regexp.MustCompile(`\{[A-Za-z0-9_.-]+\}`)

// fill is in's value: what was given for it (set into its template, if it
// has one), else its fixed value or default.
func fill(in Input, given map[string]string) string {
	v := given[in.Name]
	if in.Value != "" && placeholderRE.MatchString(in.Value) {
		if v == "" {
			return ""
		}
		// "Bearer {api_key}" with the key given for the header.
		return placeholderRE.ReplaceAllString(in.Value, v)
	}
	if v != "" {
		return v
	}
	if in.Value != "" {
		return in.Value
	}
	return in.Default
}

// Config turns o into a server entry, with given holding what was asked
// for (by Input name). Optional values left out are left out.
func (o Option) Config(given map[string]string) (Server, error) {
	for _, in := range o.Needs() {
		if strings.TrimSpace(given[in.Name]) == "" {
			return Server{}, fmt.Errorf("%s is needed: %s", in.Name, in.Description)
		}
	}
	if r := o.Remote; r != nil {
		s := Server{Type: "http", URL: r.URL}
		if r.Type == "sse" {
			s.Type = "sse"
		}
		for _, h := range r.Headers {
			if v := fill(h, given); v != "" {
				if s.Headers == nil {
					s.Headers = map[string]string{}
				}
				s.Headers[h.Name] = v
			}
		}
		return s, nil
	}
	p := o.Package
	s := Server{Command: runner[p.RegistryType]}
	args := func(list []Argument) []string {
		var out []string
		for _, a := range list {
			name := a.Name
			if name == "" {
				name = a.ValueHint
			}
			v := fill(Input{Name: name, Value: a.Value, Default: a.Default}, given)
			switch a.Type {
			case "named":
				if v == "" {
					continue
				}
				flag := a.Name
				if !strings.HasPrefix(flag, "-") {
					flag = "--" + flag
				}
				out = append(out, flag, v)
			default:
				if v != "" {
					out = append(out, v)
				}
			}
		}
		return out
	}
	env := map[string]string{}
	for _, e := range p.EnvironmentVariables {
		if v := fill(e, given); v != "" {
			env[e.Name] = v
		}
	}
	runtime := args(p.RuntimeArguments)
	switch p.RegistryType {
	case "npm":
		if !contains(runtime, "-y") && !contains(runtime, "--yes") {
			runtime = append([]string{"-y"}, runtime...)
		}
		s.Args = append(runtime, versioned(p.Identifier, p.Version))
	case "pypi":
		s.Args = append(runtime, versioned(p.Identifier, p.Version))
	case "oci":
		s.Args = append([]string{"run", "-i", "--rm"}, runtime...)
		// docker passes each variable through from the environment it
		// runs in, which is the server's env.
		for _, k := range sortedKeys(env) {
			s.Args = append(s.Args, "-e", k)
		}
		img := p.Identifier
		if p.Version != "" && !strings.Contains(img[strings.LastIndex(img, "/")+1:], ":") {
			img += ":" + p.Version
		}
		s.Args = append(s.Args, img)
	}
	s.Args = append(s.Args, args(p.PackageArguments)...)
	if len(env) > 0 {
		s.Env = env
	}
	return s, nil
}

func versioned(id, version string) string {
	if version == "" || version == "latest" {
		return id
	}
	return id + "@" + version
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
