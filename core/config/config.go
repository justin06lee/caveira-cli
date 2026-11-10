// Package config resolves where caveira sends requests and as what: the
// model server, model, reasoning effort. Sources, lowest to highest
// priority: built-in defaults, ~/.caveira/config.json, .env files between
// the repository root and the working directory, environment variables,
// and finally command-line flags (applied by the caller).
//
// Requests go through the caveira account. Only a development build
// (package devserver) can send them to a model server on this machine or
// the local network set with base_url (see CheckLocal), with a key of its
// own (api_key, CAVEIRA_API_KEY, --api-key); a shipped build ignores all
// of those. The account's token only ever goes to the caveira site.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/devserver"
	"github.com/justin06lee/caveira/core/private"
)

const DefaultModel = "qwen-3.5"

// Settings is the resolved configuration.
type Settings struct {
	// BaseURL is where model requests go. In the file and the
	// environment it is a local model server set on purpose; Load makes
	// it the caveira account's endpoint when there is none and the
	// account is signed in, and leaves it empty when neither is there.
	BaseURL         string `json:"base_url,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ContextWindow overrides the known size for the model, in tokens.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxTokens caps each completion; 0 leaves it to the server.
	MaxTokens int `json:"max_tokens,omitempty"`
	// SoftTokenLimit is how large a request may grow, in tokens, before
	// the session compacts, whatever the model's window; 0 is
	// DefaultSoftTokenLimit (CAVEIRA_SOFT_TOKEN_LIMIT sets it too).
	SoftTokenLimit int `json:"soft_token_limit,omitempty"`
	// Confirm asks before running commands or changing files.
	Confirm bool `json:"confirm,omitempty"`
	// NoUpdateCheck stops caveira asking GitHub whether a newer release
	// is out (CAVEIRA_NO_UPDATE_CHECK=1 does the same).
	NoUpdateCheck bool `json:"no_update_check,omitempty"`
	// NoAutoUpdate leaves updating to /update: a launch doesn't install a
	// newer release itself. CAVEIRA_NO_AUTO_UPDATE does the same.
	NoAutoUpdate bool `json:"no_auto_update,omitempty"`

	// LocalKey is a key for the local model server at BaseURL, for one
	// behind a proxy that wants one. It is sent there and nowhere else:
	// without a local BaseURL it is ignored, and not saved.
	LocalKey string `json:"api_key,omitempty"`

	// APIKey is what requests carry: the account's token when they go
	// through it, LocalKey when they go to a local server, and empty
	// otherwise.
	APIKey string `json:"-"`
	// KeySource is AccountSource when requests go through the account,
	// and says where LocalKey came from when that is in use.
	KeySource string `json:"-"`
}

// Dir is ~/.caveira.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".caveira"
	}
	return filepath.Join(home, ".caveira")
}

// Path is the settings file.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load resolves settings for a run rooted at workDir. A base URL that is
// not a local server is dropped, and the error says why.
func Load(workDir string) (Settings, error) {
	s := Settings{Model: DefaultModel}
	from, keyFrom := "", ""

	var errs []error
	if b, err := os.ReadFile(Path()); err == nil {
		var file Settings
		if err := json.Unmarshal(b, &file); err != nil {
			errs = append(errs, errors.New(Path()+": "+err.Error()))
		} else {
			merge(&s, file)
			if devserver.Enabled && file.BaseURL != "" {
				from = "base_url in " + Path()
			}
			if devserver.Enabled && file.LocalKey != "" {
				keyFrom = "api_key in " + Path()
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}

	// .env files: outermost first so the closest one wins.
	for _, f := range envFiles(workDir) {
		vars, err := parseEnvFile(f)
		if err != nil {
			continue
		}
		url, key := applyEnv(&s, func(k string) string { return vars[k] })
		if devserver.Enabled && url {
			from = baseURLVar + " in " + f
		}
		if devserver.Enabled && key {
			keyFrom = keyVar + " in " + f
		}
	}

	if url, key := applyEnv(&s, os.Getenv); devserver.Enabled && (url || key) {
		if url {
			from = baseURLVar
		}
		if key {
			keyFrom = keyVar
		}
	}

	// A shipped build has no model server but the account's: whatever the
	// files and the environment say about one is dropped.
	if !devserver.Enabled {
		s.BaseURL, s.LocalKey = "", ""
	}
	if s.BaseURL != "" {
		if err := CheckLocal(s.BaseURL); err != nil {
			errs = append(errs, errors.New(from+": "+err.Error()))
			s.BaseURL = ""
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	// A local server set on purpose gets its own key, if one is set.
	if s.BaseURL != "" && s.LocalKey != "" {
		s.APIKey, s.KeySource = s.LocalKey, keyFrom
	}
	// Signed in, model requests go through the caveira account. A local
	// server set on purpose is left alone, and the account's token does
	// not go to it.
	if s.BaseURL == "" {
		if a, ok := cloud.Load(); ok {
			s.BaseURL = a.APIBase()
			s.APIKey = a.Token
			s.KeySource = AccountSource
		}
	}
	return s, errors.Join(errs...)
}

// ErrAccountOnly is the answer of a shipped build asked to use a model
// server of its own: it runs on a caveira account only.
var ErrAccountOnly = errors.New("caveira runs on your caveira account only; sign in with caveira login")

// AccountSource is the KeySource of a signed-in caveira account.
const AccountSource = "caveira account"

// OnAccount says s sends its model requests through the caveira account.
func (s Settings) OnAccount() bool { return s.KeySource == AccountSource }

// Local says s sends its model requests to a local model server.
func (s Settings) Local() bool { return !s.OnAccount() && IsLocal(s.BaseURL) }

// UseLocal points s at the local model server baseURL, from --base-url,
// in place of the account or whatever the files said. A key set for a
// local server (api_key, CAVEIRA_API_KEY) goes with it; the account's
// token does not.
func (s Settings) UseLocal(baseURL string) (Settings, error) {
	if !devserver.Enabled {
		return s, ErrAccountOnly
	}
	if err := CheckLocal(baseURL); err != nil {
		return s, err
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	s.APIKey, s.KeySource = s.LocalKey, ""
	if s.LocalKey != "" {
		s.KeySource = "a local server's key"
	}
	return s, nil
}

// WithLocalKey is s with key, from --api-key, for its local model
// server. It needs one: a key never goes to the account or a hosted
// endpoint.
func (s Settings) WithLocalKey(key string) (Settings, error) {
	if !devserver.Enabled {
		return s, ErrAccountOnly
	}
	if !s.Local() {
		return s, errors.New("--api-key is for a local model server, and goes with --base-url")
	}
	s.LocalKey, s.APIKey, s.KeySource = key, key, "--api-key"
	return s, nil
}

func merge(dst *Settings, src Settings) {
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.ReasoningEffort != "" {
		dst.ReasoningEffort = src.ReasoningEffort
	}
	if src.ContextWindow > 0 {
		dst.ContextWindow = src.ContextWindow
	}
	if src.MaxTokens > 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if src.SoftTokenLimit > 0 {
		dst.SoftTokenLimit = src.SoftTokenLimit
	}
	if src.Confirm {
		dst.Confirm = true
	}
	if src.NoUpdateCheck {
		dst.NoUpdateCheck = true
	}
	if src.NoAutoUpdate {
		dst.NoAutoUpdate = true
	}
	if src.LocalKey != "" {
		dst.LocalKey = src.LocalKey
	}
}

// Recognized variable names.
const (
	baseURLVar   = "CAVEIRA_BASE_URL"
	keyVar       = "CAVEIRA_API_KEY"
	softLimitVar = "CAVEIRA_SOFT_TOKEN_LIMIT"
)

// DefaultSoftTokenLimit is the soft token limit when none is set: a
// session compacts as a request nears it, even on a model whose window
// is larger.
const DefaultSoftTokenLimit = 434_000

var (
	modelVars  = []string{"CAVEIRA_MODEL"}
	effortVars = []string{"CAVEIRA_REASONING_EFFORT", "CAVEIRA_EFFORT"}
)

// applyEnv lays the variables get has over s, and says whether they set
// the base URL and the local server's key.
func applyEnv(s *Settings, get func(string) string) (baseURL, key bool) {
	first := func(names []string) string {
		for _, n := range names {
			if v := strings.TrimSpace(get(n)); v != "" {
				return v
			}
		}
		return ""
	}
	// A shipped build doesn't read these at all.
	if devserver.Enabled {
		if v := strings.TrimSpace(get(baseURLVar)); v != "" {
			s.BaseURL = v
			baseURL = true
		}
		if v := strings.TrimSpace(get(keyVar)); v != "" {
			s.LocalKey = v
			key = true
		}
	}
	if v := first(modelVars); v != "" {
		s.Model = v
	}
	if v := first(effortVars); v != "" {
		s.ReasoningEffort = v
	}
	if v := strings.ToLower(get("CAVEIRA_CONFIRM")); v == "1" || v == "true" || v == "yes" {
		s.Confirm = true
	}
	if n, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(get(softLimitVar)), "_", "")); err == nil && n > 0 {
		s.SoftTokenLimit = n
	}
	return baseURL, key
}

// envFiles lists .env and .env.local from the git root (or the directory
// itself when not in a repo) down to workDir, outermost first.
func envFiles(workDir string) []string {
	root := gitRoot(workDir)
	if root == "" {
		root = workDir
	}
	var dirs []string
	for d := workDir; ; d = filepath.Dir(d) {
		dirs = append([]string{d}, dirs...)
		if d == root || d == filepath.Dir(d) {
			break
		}
	}
	var out []string
	for _, d := range dirs {
		for _, name := range []string{".env", ".env.local"} {
			p := filepath.Join(d, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				out = append(out, p)
			}
		}
	}
	return out
}

func gitRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// parseEnvFile reads KEY=VALUE lines. Quotes are stripped, `export` is
// allowed, comments and blank lines are skipped. Interpolation is not done.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		vars[k] = v
	}
	return vars, sc.Err()
}

// ReadFile is what the settings file alone says, without defaults, .env
// files, or the environment: the layer a settings screen edits.
func ReadFile() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, errors.New(Path() + ": " + err.Error())
	}
	return s, nil
}

// Save writes the settings file. A key without a local server to go
// to, like one an older caveira kept there, is not written back.
func Save(s Settings) error {
	if !IsLocal(s.BaseURL) {
		s.LocalKey = ""
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return private.WriteFile(Path(), append(b, '\n'))
}

// ModelSpec is what caveira knows about a model without asking the server.
type ModelSpec struct {
	ContextWindow int
	// Prices in USD per million tokens. Zero means unknown or free.
	InputPerM, CachedPerM, OutputPerM float64
}

var knownModels = map[string]ModelSpec{
	"qwen-3.5": {ContextWindow: 262_144, InputPerM: 1, CachedPerM: 0.10, OutputPerM: 3},
	"glm-5.2":  {ContextWindow: 1_000_000, InputPerM: 3, CachedPerM: 0.30, OutputPerM: 5},
	"glm-5.3":  {ContextWindow: 1_000_000, InputPerM: 3, CachedPerM: 0.30, OutputPerM: 5},
}

// Known reports whether caveira has a spec for model.
func Known(model string) bool {
	_, ok := knownModels[model]
	return ok
}

// KnownModels lists the models caveira has specs for, sorted.
func KnownModels() []string {
	out := make([]string, 0, len(knownModels))
	for id := range knownModels {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Spec returns what is known about model, falling back to a conservative
// context window for anything unrecognized (local models, other gateways).
func Spec(model string) ModelSpec {
	if s, ok := knownModels[model]; ok {
		return s
	}
	return ModelSpec{ContextWindow: 128_000}
}

// Efforts are the reasoning levels caveira accepts. Each model maps
// them onto the modes it actually has; "none" turns reasoning off where the
// model allows it.
var Efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func ValidEffort(e string) bool {
	for _, v := range Efforts {
		if v == e {
			return true
		}
	}
	return false
}
