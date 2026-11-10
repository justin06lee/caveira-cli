package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Bundle is the .app the executable exe runs from, or "" when it is not
// in one (a development build run from the repository, Linux).
func Bundle(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// …/caveira.app/Contents/MacOS/caveira
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}

// ManualError is an app update caveira could not put in place itself:
// the disk image is open in the Finder for the person to drag across.
type ManualError struct{ Reason string }

func (e *ManualError) Error() string {
	return e.Reason + ": the disk image is open; drag caveira to Applications"
}

// ReplaceApp copies the bundle src over dst with ditto, by way of a
// sibling copy renamed into place, so a failure halfway leaves the old
// app whole. The old app stays at AppBackup.
func ReplaceApp(src, dst string) error {
	parent := filepath.Dir(dst)
	staged := filepath.Join(parent, "."+filepath.Base(dst)+".new")
	old := filepath.Join(parent, "."+filepath.Base(dst)+".old")
	os.RemoveAll(staged)
	os.RemoveAll(old)
	if out, err := exec.Command("ditto", src, staged).CombinedOutput(); err != nil {
		os.RemoveAll(staged)
		return fmt.Errorf("ditto: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.RemoveAll(staged)
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		os.Rename(old, dst)
		os.RemoveAll(staged)
		return err
	}
	return nil
}

// AppBackup is where ReplaceApp leaves the app it replaced, for as long as
// the new one hasn't proved it starts.
func AppBackup(bundle string) string {
	return filepath.Join(filepath.Dir(bundle), "."+filepath.Base(bundle)+".old")
}

// checkAppVersion refuses an app that says it is not the release it came
// in. An app whose Info.plist doesn't say is let through: the signature
// has already been checked.
func checkAppVersion(app, version string) error {
	out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist")).Output()
	if err != nil {
		return nil
	}
	if got := strings.TrimSpace(string(out)); got != "" && got != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("the new app says it is %s, not %s", got, version)
	}
	return nil
}

// AppRequirement is the code signature a new app must carry before it
// replaces the running one: caveira's identifier, signed with a Developer
// ID certificate Apple issued to caveira's team. A disk image that passes
// its checksum but holds an app signed by anyone else is refused.
const AppRequirement = `identifier "dev.caveira.desktop" and anchor apple generic` +
	` and certificate 1[field.1.2.840.113635.100.6.2.6] exists` +
	` and certificate leaf[field.1.2.840.113635.100.6.1.13] exists` +
	` and certificate leaf[subject.OU] = "65UN46K6FA"`

// VerifyApp checks the whole bundle app is sealed and signed to meet
// requirement (AppRequirement when empty).
func VerifyApp(ctx context.Context, app, requirement string) error {
	if err := Unsupported(); err != nil {
		return err
	}
	if requirement == "" {
		requirement = AppRequirement
	}
	out, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", "-R="+requirement, app).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the new app's signature is not caveira's: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CanReplaceApp says caveira can put a new app in place of bundle.
func CanReplaceApp(bundle string) bool {
	return bundle != "" && writable(filepath.Dir(bundle)) && writable(bundle)
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".caveira-write-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// InstallApp downloads release rel's disk image, checks it against the
// release's checksums and the app in it against Requirement, and puts the
// app in place of bundle (macOS only). Where bundle's folder is not
// writable, the image is opened in the Finder instead and a ManualError
// comes back. The terminal client is updated too where install.sh put
// one. The caller relaunches.
func (c *Client) InstallApp(ctx context.Context, rel Release, bundle string) error {
	if err := Unsupported(); err != nil {
		return err
	}
	if bundle == "" {
		return errors.New("caveira is not running from an app bundle")
	}
	work, err := os.MkdirTemp("", "caveira-update-")
	if err != nil {
		return err
	}
	dmg, err := c.Download(ctx, rel, DMG, work)
	if err != nil {
		os.RemoveAll(work)
		return err
	}
	if !writable(filepath.Dir(bundle)) || !writable(bundle) {
		// The image is left for the Finder; the temp folder goes with
		// the next reboot.
		exec.Command("open", dmg).Start()
		return &ManualError{Reason: "caveira cannot write to " + filepath.Dir(bundle)}
	}
	defer os.RemoveAll(work)

	mount := filepath.Join(work, "mnt")
	if err := os.Mkdir(mount, 0o700); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, dmg).CombinedOutput(); err != nil {
		return fmt.Errorf("cannot open the disk image: %v: %s", err, strings.TrimSpace(string(out)))
	}
	defer exec.Command("hdiutil", "detach", "-force", mount).Run()

	apps, _ := filepath.Glob(filepath.Join(mount, "*.app"))
	if len(apps) == 0 {
		return errors.New("the disk image has no app in it")
	}
	if err := VerifyApp(ctx, apps[0], c.Requirement); err != nil {
		return err
	}
	if err := checkAppVersion(apps[0], rel.Version); err != nil {
		return err
	}
	if err := ReplaceApp(apps[0], bundle); err != nil {
		return err
	}
	// A terminal client installed by install.sh moves to the same release.
	if cli := CLIPath(); fileExists(cli) {
		if err := c.ReplaceBinary(ctx, rel, cli); err != nil {
			return &CLIError{Err: err}
		}
		AttemptedBinary(cli, rel.Version, "")
	}
	return nil
}

// CLIError is an app update that went in while the terminal client
// beside it could not be updated.
type CLIError struct{ Err error }

func (e *CLIError) Error() string {
	return "the app is updated, but not the terminal client: " + e.Err.Error()
}

func (e *CLIError) Unwrap() error { return e.Err }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
