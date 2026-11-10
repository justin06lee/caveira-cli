package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/config"
)

// REA (github.com/morluto/rea) is the one server caveira keeps up to date
// itself. It is third-party code, so nothing of it runs that has not been
// checked: a launch looks for the newest release on GitHub, takes the npm
// package built for that tag, and installs it only if Sigstore's record
// says morluto/rea's release workflow built that exact file from the
// commit the tag points at (reaverify.go). A version starts in a check
// of its own before it is used; the version before it is kept, and goes
// back in if the new one can't start in a real session.
//
// Until then a user has the npx entry SeedDefaults puts in mcp.json; the
// first verified install turns it into the managed entry, ReaServer.
// An entry the user changed by hand, turned off or removed is left as it
// is, and nothing is downloaded for it.

const (
	reaRepo      = "morluto/rea"
	reaPackage   = "rea-agents"
	reaTagPrefix = "rea-agents-"
	// ReaVar is the variable a managed entry names the installed
	// version's entry script by; the manager fills it in at start.
	ReaVar = "CAVEIRA_REA"
)

// ReaServer is the mcp.json entry for the rea caveira manages.
var ReaServer = Server{Command: "node", Args: []string{"${" + ReaVar + "}", "mcp"}}

// managedRea says s is the rea caveira installs and updates.
func managedRea(s Server) bool {
	return s.Transport() == "stdio" && slices.ContainsFunc(s.Args, func(a string) bool {
		return strings.Contains(a, "${"+ReaVar+"}")
	})
}

var seededReaArg = regexp.MustCompile(`^rea-agents@\d+\.\d+\.\d+$`)

// seededRea says s is the npx entry SeedDefaults puts in, as it was put
// in: no other arguments, environment or type.
func seededRea(s Server) bool {
	return s.Command == "npx" && s.Type == "" && s.URL == "" && len(s.Env) == 0 && len(s.Headers) == 0 &&
		len(s.Args) == 3 && s.Args[0] == "-y" && seededReaArg.MatchString(s.Args[1]) && s.Args[2] == "mcp"
}

// ReaDir is where rea's versions and their record live.
func ReaDir() string { return filepath.Join(config.Dir(), "tools", "rea") }

func reaVersionsDir() string        { return filepath.Join(ReaDir(), "versions") }
func reaVersionDir(v string) string { return filepath.Join(reaVersionsDir(), v) }

// reaScript is the entry script of installed version v.
func reaScript(v string) string {
	return filepath.Join(reaVersionDir(v), "node_modules", reaPackage, "scripts", "rea.mjs")
}

func reaInstalled(v string) bool {
	if v == "" {
		return false
	}
	_, err := os.Stat(reaScript(v))
	return err == nil
}

// reaState is ~/.caveira/tools/rea/state.json.
type reaState struct {
	// Current is the version a session starts; Previous the one before
	// it, kept so a Current that can't start can be undone.
	Current  string `json:"current,omitempty"`
	Previous string `json:"previous,omitempty"`
	// Pins are every version verified so far: the tag, the commit it
	// pointed at and the package's SHA-512. A version whose package
	// later comes back different is refused.
	Pins map[string]reaPin `json:"pins,omitempty"`
	// Bad are versions that failed, and why. A launch doesn't install
	// one again; a newer release is looked at as usual.
	Bad map[string]string `json:"bad,omitempty"`
	// Retired are versions no longer current or previous, and since
	// when. Their folders go a few days later, so a long session still
	// running one keeps its files.
	Retired map[string]time.Time `json:"retired,omitempty"`
	// ETag and Latest are GitHub's last answer about the newest
	// release, so a launch with nothing new costs no rate limit.
	ETag   string `json:"etag,omitempty"`
	Latest string `json:"latest,omitempty"`
	// Rollback is the last time a version was undone.
	Rollback *reaRollback `json:"rollback,omitempty"`
	// Checked is the last launch's look, and what came of it.
	Checked *reaCheck `json:"checked,omitempty"`
}

type reaPin struct {
	Tag      string    `json:"tag"`
	Commit   string    `json:"commit"`
	SHA512   string    `json:"sha512"`
	Verified time.Time `json:"verified"`
}

type reaRollback struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	Why  string    `json:"why"`
	At   time.Time `json:"at"`
}

type reaCheck struct {
	At     time.Time `json:"at"`
	Result string    `json:"result"`
}

func reaStatePath() string { return filepath.Join(ReaDir(), "state.json") }

func loadReaState() reaState {
	var st reaState
	if b, err := os.ReadFile(reaStatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// save writes the record whole, through a new file renamed over the old
// one, so a reader sees the old record or the new one and never half.
func (st reaState) save() error {
	if err := os.MkdirAll(ReaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(ReaDir(), ".state-*.json")
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), reaStatePath()); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// ReaVersion is the rea version sessions start now, or "" before the
// first verified install.
func ReaVersion() string {
	st := loadReaState()
	if reaInstalled(st.Current) {
		return st.Current
	}
	return ""
}

// reaStarted records that v started in a session, with a file in its
// folder, so no session waits on an install holding the lock.
func reaStarted(v string) {
	if !reaHealthy(v) {
		_ = os.WriteFile(filepath.Join(reaVersionDir(v), startedMark), nil, 0o600)
	}
}

const startedMark = ".caveira-started"

// reaHealthy says v has started in a real session. Only a version that
// never has is rolled back when it fails to start.
func reaHealthy(v string) bool {
	_, err := os.Stat(filepath.Join(reaVersionDir(v), startedMark))
	return err == nil
}

// reaFailed is a session's report that version v failed to start. A v
// that has never started in a session, with an installed version before
// it, is undone: the previous version goes back and v is marked bad.
// It says the version to try instead, if any.
func reaFailed(v, why string) (string, bool) {
	unlock, err := reaLock(5 * time.Second)
	if err != nil {
		return "", false
	}
	defer unlock()
	st := loadReaState()
	if st.Current != v || reaHealthy(v) || !reaInstalled(st.Previous) {
		return "", false
	}
	if st.Bad == nil {
		st.Bad = map[string]string{}
	}
	st.Bad[v] = "failed to start: " + why
	st.Rollback = &reaRollback{From: v, To: st.Previous, Why: why, At: time.Now().UTC()}
	st.Current, st.Previous = st.Previous, ""
	if err := st.save(); err != nil {
		return "", false
	}
	_ = os.RemoveAll(reaVersionDir(v))
	return st.Current, true
}

// reaLock keeps two caveiras (the terminal and the app, say) from
// installing at once. It waits up to wait for the other to finish; a lock
// older than ten minutes is from a process that died and is taken over.
func reaLock(wait time.Duration) (func(), error) {
	if err := os.MkdirAll(ReaDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(ReaDir(), ".lock")
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if info, serr := os.Stat(path); serr == nil && time.Since(info.ModTime()) > 10*time.Minute {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("another caveira is installing rea")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// reaSemver is v as three numbers, ok false for anything else (a
// pre-release, say).
func reaSemver(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || strconv.Itoa(x) != p {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// reaNewer says a is a later version than b. Anything is newer than "".
func reaNewer(a, b string) bool {
	na, ok := reaSemver(a)
	if !ok {
		return false
	}
	nb, ok := reaSemver(b)
	if !ok {
		return true
	}
	return slices.Compare(na[:], nb[:]) > 0
}
