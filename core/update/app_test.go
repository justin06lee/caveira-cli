package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// signedApp makes dir/caveira.app at version, its executable a copy of
// a real Mach-O, signed ad hoc the way make desktop signs a build. With
// CAVEIRA_E2E_APP set to a built caveira.app, that bundle is used instead
// and only its version changes. With CAVEIRA_SIGN_ID set to a Developer ID
// Application identity, the next release is signed with it the way make
// dmg signs one, and must meet AppRequirement.
func signedApp(t *testing.T, dir, version string) string {
	return signApp(t, dir, version, "-")
}

func signApp(t *testing.T, dir, version, identity string) string {
	t.Helper()
	app := filepath.Join(dir, "caveira.app")
	if real := os.Getenv("CAVEIRA_E2E_APP"); real != "" {
		run(t, "ditto", real, app)
	} else {
		macos := filepath.Join(app, "Contents", "MacOS")
		os.MkdirAll(macos, 0o755)
		run(t, "cp", "/usr/bin/true", filepath.Join(macos, "caveira"))
		os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>caveira</string>
<key>CFBundleIdentifier</key><string>dev.caveira.desktop</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0</string>
<key>CFBundleVersion</key><string>0</string>
</dict></plist>
`), 0o644)
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	run(t, "/usr/libexec/PlistBuddy", "-c", "Set :CFBundleShortVersionString "+version, "-c", "Set :CFBundleVersion "+version, plist)
	if identity == "-" {
		run(t, "codesign", "--force", "--deep", "--sign", "-", app)
	} else {
		// With the real home, where the keychain holding identity is.
		cmd := exec.Command("codesign", "--force", "--deep", "--options", "runtime", "--timestamp=none", "--sign", identity, app)
		cmd.Env = append(os.Environ(), "HOME="+realHome)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("codesign --sign %s: %v\n%s", identity, err, out)
		}
	}
	return app
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func bundleVersion(t *testing.T, app string) string {
	return run(t, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", filepath.Join(app, "Contents", "Info.plist"))
}

// release is the next release, 9.9.9, served the way GitHub serves one:
// an app in a disk image (signed by signer, "-" for ad hoc), a terminal
// client, and the checksums of both. dir/Applications/caveira.app is this
// release, 1.0.0, installed beside install.sh's terminal client.
type release struct {
	dir, installed, bin string
	client              *Client
}

func newRelease(t *testing.T, signer string) *release {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("disk images are macOS's")
	}
	if testing.Short() {
		t.Skip("builds and mounts a disk image")
	}
	dir := t.TempDir()

	stage := filepath.Join(dir, "stage")
	os.MkdirAll(stage, 0o755)
	signApp(t, stage, "9.9.9", signer)
	os.Symlink("/Applications", filepath.Join(stage, "Applications"))
	dmg := filepath.Join(dir, "caveira.dmg")
	run(t, "hdiutil", "create", "-quiet", "-volname", "caveira", "-srcfolder", stage, "-format", "UDZO", "-ov", dmg)
	image, err := os.ReadFile(dmg)
	if err != nil {
		t.Fatal(err)
	}
	client := tarball(t, "caveira", "#!new client 9.9.9")
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[
				{"name":"caveira.dmg","browser_download_url":"%[1]s/dl/caveira.dmg"},
				{"name":%[2]q,"browser_download_url":"%[1]s/dl/client"},
				{"name":"checksums.txt","browser_download_url":"%[1]s/dl/checksums.txt"}]}`, "http://"+r.Host, name)
		case "/dl/caveira.dmg":
			w.Write(image)
		case "/dl/client":
			w.Write(client)
		case "/dl/checksums.txt":
			fmt.Fprintf(w, "%s  caveira.dmg\n%s  %s\n", sum(image), sum(client), name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	apps := filepath.Join(dir, "Applications")
	os.MkdirAll(apps, 0o755)
	installed := signedApp(t, apps, "1.0.0")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "caveira"), []byte("#!old client"), 0o755)
	t.Setenv("CAVEIRA_INSTALL_DIR", bin)

	return &release{dir: dir, installed: installed, bin: bin,
		client: &Client{CheckBinary: func(context.Context, string, string) error { return nil }, Latest: srv.URL + "/latest", Cache: filepath.Join(dir, "update.json")}}
}

// install runs the update the window's Update button runs.
func (r *release) install(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if latest, err := r.client.Check(ctx, "v1.0.0", 0); err != nil || latest != "v9.9.9" {
		t.Fatalf("Check = %q, %v", latest, err)
	}
	rel, err := r.client.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return r.client.InstallApp(ctx, rel, r.installed)
}

// tidy fails when the update left its disk image attached or a
// staged copy beside the app.
func (r *release) tidy(t *testing.T) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(r.installed), ".*"))
	for _, l := range left {
		if l != AppBackup(r.installed) {
			t.Fatalf("left behind %v", left)
		}
	}
	if out := run(t, "hdiutil", "info"); strings.Contains(out, "/caveira-update-") {
		t.Fatalf("disk image still attached:\n%s", out)
	}
}

// TestInstallAppFromDiskImage takes an installed app from one release to
// the next the way the window's Update button does: the latest release's
// caveira.dmg is fetched and checked, mounted with hdiutil, its app's
// signature checked and the app put in place of the old one, and the
// terminal client beside it updated.
func TestInstallAppFromDiskImage(t *testing.T) {
	skipOnWindows(t)
	signer := os.Getenv("CAVEIRA_SIGN_ID")
	if signer == "" {
		signer = "-"
	}
	r := newRelease(t, signer)
	if signer == "-" {
		// An ad hoc signature carries no certificate, only the identifier.
		r.client.Requirement = `identifier "dev.caveira.desktop"`
	}
	if err := r.install(t); err != nil {
		t.Fatal(err)
	}

	if v := bundleVersion(t, r.installed); v != "9.9.9" {
		t.Fatalf("installed app is %s, want 9.9.9", v)
	}
	// A swapped bundle that no longer verifies would not open.
	run(t, "codesign", "--verify", "--deep", "--strict", r.installed)
	if b, _ := os.ReadFile(filepath.Join(r.bin, "caveira")); string(b) != "#!new client 9.9.9" {
		t.Fatalf("terminal client holds %q", b)
	}
	r.tidy(t)
}

// TestInstallAppRefusesAnotherSignature: a release whose checksums match
// but whose app is not signed with caveira's Developer ID is not put in
// place, and neither is its terminal client.
func TestInstallAppRefusesAnotherSignature(t *testing.T) {
	skipOnWindows(t)
	r := newRelease(t, "-")
	err := r.install(t)
	if err == nil || !strings.Contains(err.Error(), "signature is not caveira's") {
		t.Fatalf("InstallApp = %v, want a refused signature", err)
	}
	if v := bundleVersion(t, r.installed); v != "1.0.0" {
		t.Fatalf("installed app is %s, want 1.0.0 left alone", v)
	}
	run(t, "codesign", "--verify", "--deep", "--strict", r.installed)
	if b, _ := os.ReadFile(filepath.Join(r.bin, "caveira")); string(b) != "#!old client" {
		t.Fatalf("terminal client holds %q", b)
	}
	r.tidy(t)
}
