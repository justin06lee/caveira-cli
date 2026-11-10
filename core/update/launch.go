package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/config"
)

// Updating at launch. The rule is that a launch always ends with caveira
// running: offline, a slow answer, a bad download, a failed signature or a
// folder caveira can't write to all mean the current version starts as
// usual, and the update waits for /update. A session already running is
// never touched: a new binary is renamed over the old path, and a running
// process keeps the file it started from.
//
// A new version that can't start must not cost the user caveira, or send
// the launch round in a loop. Before the swap the new binary has to answer
// --version. After it, the new version counts its own starts in the launch
// state and marks itself healthy once it is up; one that finds it started
// before without getting that far puts the old version back and is skipped
// until a newer release comes out.

// The two things that update themselves at launch.
const (
	Terminal = "terminal"
	App      = "app"
)

// UpdatedFromEnv is set on the new version a launch starts, naming the
// version it replaced, so it doesn't look for yet another update.
const UpdatedFromEnv = "CAVEIRA_UPDATED_FROM"

// LaunchCheckTimeout is how long a launch waits to hear whether there is a
// newer release, and LaunchDownloadTimeout how long the terminal client
// then waits for it.
const (
	LaunchCheckTimeout    = 2 * time.Second
	LaunchDownloadTimeout = 60 * time.Second
)

// AutoDisabled says updating at launch is turned off, by
// CAVEIRA_NO_AUTO_UPDATE or no_auto_update in the settings file. Turning
// update checks off turns it off too.
func AutoDisabled() bool {
	return Disabled() || OptedOut()
}

// OptedOut says the user turned updating at launch off, or update checks
// altogether: what AutoDisabled says, without a system caveira can't
// replace itself on. The built-in rea server (core/mcp) updates itself by
// this, on Windows too.
func OptedOut() bool {
	if checksOff() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CAVEIRA_NO_AUTO_UPDATE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	s, _ := config.ReadFile()
	return s.NoAutoUpdate
}

// launchState is what a launch update leaves for the version it starts.
type launchState struct {
	// Attempted is the version a launch put in place, From what it
	// replaced, and Backup where that is kept until Attempted is healthy.
	Attempted string `json:"attempted,omitempty"`
	From      string `json:"from,omitempty"`
	Backup    string `json:"backup,omitempty"`
	// Starts counts Attempted's starts; Healthy says it got all the way up.
	Starts  int  `json:"starts,omitempty"`
	Healthy bool `json:"healthy,omitempty"`
	// Skip is a version that failed to start. Launches don't update to it.
	Skip string `json:"skip,omitempty"`
}

func statePath() string { return filepath.Join(config.Dir(), "update-launch.json") }

func loadStates() map[string]launchState {
	states := map[string]launchState{}
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &states)
	}
	return states
}

func saveState(what string, st launchState) {
	states := loadStates()
	states[what] = st
	b, err := json.Marshal(states)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(statePath()), 0o700)
	tmp := statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, statePath())
	}
}

// Attempted records that a launch put version in place of from, with the
// old one kept at backup.
func Attempted(what, version, from, backup string) {
	st := loadStates()[what]
	st.Attempted, st.From, st.Backup, st.Starts, st.Healthy = version, from, backup, 0, false
	if st.Skip == version {
		st.Skip = ""
	}
	saveState(what, st)
}

// Skipped says a launch shouldn't update to version.
func Skipped(what, version string) bool { return loadStates()[what].Skip == version }

// Started counts a start of version. It says to roll back when this is the
// version a launch put in place and an earlier start of it never got up.
func Started(what, version string) (rollBack bool) {
	st := loadStates()[what]
	if st.Attempted != version || st.Healthy {
		return false
	}
	st.Starts++
	saveState(what, st)
	return st.Starts > 1
}

// Healthy says version got all the way up, and lets the backup go.
func Healthy(what, version string) {
	st := loadStates()[what]
	if st.Attempted != version || st.Healthy {
		return
	}
	st.Healthy = true
	if st.Backup != "" {
		os.RemoveAll(st.Backup)
		st.Backup = ""
	}
	saveState(what, st)
}

// RollBack puts the kept version back over target and marks the attempted
// one to be skipped. It answers false when there is nothing to put back.
func RollBack(what, target string) bool {
	st := loadStates()[what]
	if st.Backup == "" {
		return false
	}
	if _, err := os.Stat(st.Backup); err != nil {
		return false
	}
	if what == App {
		// A bundle is a folder: the broken one has to move out of the way.
		broken := target + ".broken"
		os.RemoveAll(broken)
		if err := os.Rename(target, broken); err != nil {
			return false
		}
		if err := os.Rename(st.Backup, target); err != nil {
			os.Rename(broken, target)
			return false
		}
		os.RemoveAll(broken)
	} else if err := os.Rename(st.Backup, target); err != nil {
		return false
	}
	st.Skip, st.Backup, st.Healthy = st.Attempted, "", true
	saveState(what, st)
	return true
}

// LaunchOutcome is what a launch did about updates.
type LaunchOutcome struct {
	// Updated is the version now at the running binary's path, to start.
	Updated string
	// Available is a newer release that was not installed, to mention.
	Available string
	// Err says why, when one should have been and wasn't.
	Err error
}

// LaunchBinary updates the terminal client at exe when a newer release is
// out. It answers within LaunchCheckTimeout when there isn't one, and the
// caller starts the current version whatever comes back unless Updated is
// set. say is told before a download starts.
func (c *Client) LaunchBinary(current, exe string, say func(string)) LaunchOutcome {
	if !IsRelease(current) || AutoDisabled() { // AutoDisabled is always true on Windows
		return LaunchOutcome{}
	}
	check, cancel := context.WithTimeout(context.Background(), LaunchCheckTimeout)
	defer cancel()
	rel, err := c.FetchLatest(check)
	if err != nil || !Newer(rel.Version, current) {
		return LaunchOutcome{}
	}
	if Skipped(Terminal, rel.Version) {
		return LaunchOutcome{Available: rel.Version}
	}
	say("updating caveira to " + rel.Version + "...")
	get, cancel2 := context.WithTimeout(context.Background(), LaunchDownloadTimeout)
	defer cancel2()
	if err := c.ReplaceBinary(get, rel, exe); err != nil {
		return LaunchOutcome{Available: rel.Version, Err: err}
	}
	AttemptedBinary(exe, rel.Version, current)
	return LaunchOutcome{Updated: rel.Version}
}

// AttemptedBinary is Attempted for a terminal client just put at exe.
func AttemptedBinary(exe, version, from string) {
	Attempted(Terminal, version, from, backupPath(ResolveBinary(exe)))
}

// ResolveBinary is the file behind exe, which may be a link such as cav.
func ResolveBinary(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// FetchLatest asks for the latest release, and notes the answer for Check.
func (c *Client) FetchLatest(ctx context.Context) (Release, error) {
	rel, err := c.Fetch(ctx)
	if err != nil {
		return Release{}, err
	}
	if b, err := json.Marshal(cached{Checked: c.now(), Latest: rel.Version}); err == nil {
		_ = os.MkdirAll(filepath.Dir(c.cachePath()), 0o700)
		_ = os.WriteFile(c.cachePath(), b, 0o600)
	}
	return rel, nil
}

// backupPath is where the version a binary replaced is kept: beside it so
// the rename back is atomic, and hidden so it isn't on anyone's PATH.
func backupPath(target string) string {
	return filepath.Join(filepath.Dir(target), ".caveira.old")
}

// keepBackup keeps the file at target at its backup path, leaving target
// in place: there is never a moment with no caveira at the path.
func keepBackup(target string) error {
	backup := backupPath(target)
	os.Remove(backup)
	if err := os.Link(target, backup); err == nil {
		return nil
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	return os.WriteFile(backup, b, 0o755)
}

// CLIRequirement is the code signature a new terminal client must carry on
// a Mac; see AppRequirement.
const CLIRequirement = `identifier "dev.caveira.cli" and anchor apple generic` +
	` and certificate 1[field.1.2.840.113635.100.6.2.6] exists` +
	` and certificate leaf[field.1.2.840.113635.100.6.1.13] exists` +
	` and certificate leaf[subject.OU] = "65UN46K6FA"`

// checkBinary is the last look at a new terminal client before it goes in:
// its signature on a Mac, and that it runs and says it is the release.
func (c *Client) checkBinary(ctx context.Context, path, version string) error {
	if c.CheckBinary != nil {
		return c.CheckBinary(ctx, path, version)
	}
	if err := Unsupported(); err != nil {
		return err // never run a downloaded program on Windows
	}
	if isDarwin {
		if out, err := exec.CommandContext(ctx, "codesign", "--verify", "--strict", "-R="+CLIRequirement, path).CombinedOutput(); err != nil {
			return fmt.Errorf("the new caveira's signature is not caveira's: %s", strings.TrimSpace(string(out)))
		}
	}
	run, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(run, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("the new caveira does not run: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != "caveira "+version {
		return fmt.Errorf("the new caveira says it is %q, not %s", got, version)
	}
	return nil
}
