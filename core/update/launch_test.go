package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func launchSetup(t *testing.T, current string) (*Client, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("CAVEIRA_NO_UPDATE_CHECK", "")
	t.Setenv("CAVEIRA_NO_AUTO_UPDATE", "")
	t.Setenv("CAVEIRA_RELEASES_URL", "")
	c := newFake(t, "v0.3.1").client(t)
	bin := filepath.Join(t.TempDir(), "caveira")
	os.WriteFile(bin, []byte("old"), 0o755)
	return c, bin
}

func content(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestLaunchInstallsANewerRelease(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	said := ""
	o := c.LaunchBinary("v0.3.0", bin, func(s string) { said = s })
	if o.Updated != "v0.3.1" || o.Err != nil {
		t.Fatalf("outcome %+v", o)
	}
	if said != "updating caveira to v0.3.1..." {
		t.Errorf("said %q", said)
	}
	if content(bin) == "old" {
		t.Error("binary not replaced")
	}
	if content(backupPath(bin)) != "old" {
		t.Error("the old binary was not kept")
	}
}

func TestLaunchLeavesTheCurrentVersionWhenThereIsNothingNewer(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.1")
	if o := c.LaunchBinary("v0.3.1", bin, func(string) { t.Error("said something") }); o != (LaunchOutcome{}) {
		t.Fatalf("outcome %+v", o)
	}
	if content(bin) != "old" {
		t.Error("binary changed")
	}
}

func TestLaunchOfflineStartsTheCurrentVersion(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	c.Latest = "http://127.0.0.1:1/latest"
	if o := c.LaunchBinary("v0.3.0", bin, func(string) {}); o != (LaunchOutcome{}) {
		t.Fatalf("outcome %+v", o)
	}
	if content(bin) != "old" {
		t.Error("binary changed")
	}
}

func TestLaunchBadBinaryIsNotSwappedIn(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	bad := errors.New("does not run")
	c.CheckBinary = func(context.Context, string, string) error { return bad }
	o := c.LaunchBinary("v0.3.0", bin, func(string) {})
	if o.Updated != "" || o.Available != "v0.3.1" || !errors.Is(o.Err, bad) {
		t.Fatalf("outcome %+v", o)
	}
	if content(bin) != "old" {
		t.Error("a bad binary replaced the good one")
	}
}

func TestLaunchUnwritableFolderStartsTheCurrentVersion(t *testing.T) {
	skipOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	c, bin := launchSetup(t, "v0.3.0")
	dir := filepath.Dir(bin)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	o := c.LaunchBinary("v0.3.0", bin, func(string) {})
	if o.Updated != "" || o.Available != "v0.3.1" {
		t.Fatalf("outcome %+v", o)
	}
	var nw *NotWritableError
	if !errors.As(o.Err, &nw) {
		t.Errorf("err = %v", o.Err)
	}
}

func TestLaunchOptOuts(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	t.Setenv("CAVEIRA_NO_AUTO_UPDATE", "1")
	if o := c.LaunchBinary("v0.3.0", bin, func(string) {}); o != (LaunchOutcome{}) {
		t.Fatalf("env: outcome %+v", o)
	}
	t.Setenv("CAVEIRA_NO_AUTO_UPDATE", "")
	dir := filepath.Join(os.Getenv("HOME"), ".caveira")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"no_auto_update": true}`), 0o600)
	if !AutoDisabled() {
		t.Fatal("no_auto_update in config.json ignored")
	}
	if Disabled() {
		t.Fatal("no_auto_update should leave the update notice on")
	}
	if o := c.LaunchBinary("v0.3.0", bin, func(string) {}); o != (LaunchOutcome{}) {
		t.Fatalf("config: outcome %+v", o)
	}
	if content(bin) != "old" {
		t.Error("binary changed")
	}
}

// A version that never gets up is rolled back on its next start and not
// tried again, so a bad release can't send launches round in a loop.
func TestFailedStartRollsBackOnceAndIsSkipped(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	if o := c.LaunchBinary("v0.3.0", bin, func(string) {}); o.Updated != "v0.3.1" {
		t.Fatalf("outcome %+v", o)
	}
	if Started(Terminal, "v0.3.1") {
		t.Fatal("first start asked to roll back")
	}
	if !Started(Terminal, "v0.3.1") {
		t.Fatal("second start without getting up did not ask to roll back")
	}
	if !RollBack(Terminal, bin) {
		t.Fatal("no rollback")
	}
	if content(bin) != "old" {
		t.Errorf("binary after rollback: %q", content(bin))
	}
	if Started(Terminal, "v0.3.0") {
		t.Error("the old version asked to roll back")
	}
	o := c.LaunchBinary("v0.3.0", bin, func(string) { t.Error("tried the bad version again") })
	if o.Available != "v0.3.1" || o.Updated != "" {
		t.Fatalf("outcome %+v", o)
	}
	if content(bin) != "old" {
		t.Error("binary changed")
	}
}

func TestHealthyStartKeepsTheNewVersion(t *testing.T) {
	skipOnWindows(t)
	c, bin := launchSetup(t, "v0.3.0")
	c.LaunchBinary("v0.3.0", bin, func(string) {})
	Started(Terminal, "v0.3.1")
	Healthy(Terminal, "v0.3.1")
	if Started(Terminal, "v0.3.1") {
		t.Error("a healthy version asked to roll back")
	}
	if _, err := os.Stat(backupPath(bin)); err == nil {
		t.Error("the backup outlived a healthy start")
	}
}
