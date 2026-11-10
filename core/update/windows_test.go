package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

// skipOnWindows is for the tests of updating itself, which caveira does
// not do on Windows: see TestWindowsNeverUpdates.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("caveira does not update itself on Windows")
	}
}

// Windows is where a running program cannot be written over and the
// release feed has no build to offer. Whatever the feed says, caveira
// there asks nothing, downloads nothing, runs nothing it downloaded, and
// says why when asked to update.
func TestWindowsNeverUpdates(t *testing.T) {
	old := goos
	goos = "windows"
	defer func() { goos = old }()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	for _, k := range []string{"CAVEIRA_NO_UPDATE_CHECK", "CAVEIRA_NO_AUTO_UPDATE", "CAVEIRA_RELEASES_URL"} {
		t.Setenv(k, "")
	}

	// A feed like the real one: a newer release, builds for the Mac and
	// Linux, none for Windows.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"x","assets":[
			{"name":"caveira_darwin_arm64.tar.gz","browser_download_url":"http://x/a"},
			{"name":"caveira_linux_amd64.tar.gz","browser_download_url":"http://x/b"},
			{"name":"checksums.txt","browser_download_url":"http://x/c"}]}`))
	}))
	defer srv.Close()
	checked := false
	c := &Client{
		Latest:      srv.URL,
		Cache:       filepath.Join(t.TempDir(), "update.json"),
		CheckBinary: func(context.Context, string, string) error { checked = true; return nil },
	}
	ctx := context.Background()

	if err := Unsupported(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Unsupported = %v", err)
	}
	if !Disabled() || !AutoDisabled() {
		t.Error("checks and launch updates are not off")
	}
	if got, err := c.Check(ctx, "v0.1.0", 0); got != "" || err != nil {
		t.Errorf("Check = %q, %v", got, err)
	}
	bin := filepath.Join(t.TempDir(), "caveira.exe")
	os.WriteFile(bin, []byte("old"), 0o755)
	said := false
	if o := c.LaunchBinary("v0.1.0", bin, func(string) { said = true }); o != (LaunchOutcome{}) || said {
		t.Errorf("LaunchBinary = %+v", o)
	}
	if hits.Load() != 0 {
		t.Errorf("the feed was asked %d times", hits.Load())
	}
	rel := Release{Version: "v9.9.9"}
	if err := c.ReplaceBinary(ctx, rel, bin); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ReplaceBinary = %v", err)
	}
	if _, err := c.Download(ctx, rel, "caveira_windows_amd64.tar.gz", t.TempDir()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Download = %v", err)
	}
	if err := c.InstallApp(ctx, rel, `C:\Apps\caveira.app`); !errors.Is(err, ErrUnsupported) {
		t.Errorf("InstallApp = %v", err)
	}
	if err := VerifyApp(ctx, `C:\Apps\caveira.app`, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("VerifyApp = %v", err) // codesign is not run
	}
	if err := (&Client{}).checkBinary(ctx, bin, "v9.9.9"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("checkBinary = %v", err) // nor is a downloaded program
	}
	if checked || string(mustRead(t, bin)) != "old" {
		t.Error("something was installed")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
