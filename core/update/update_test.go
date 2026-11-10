package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tarball(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeReleases serves a latest release with the terminal client for
// this platform. corrupt makes checksums.txt disagree with it.
type fakeReleases struct {
	*httptest.Server
	version string
	asset   []byte
	corrupt bool
	hits    atomic.Int32
}

func newFake(t *testing.T, version string) *fakeReleases {
	f := &fakeReleases{version: version, asset: tarball(t, "caveira", "#!new binary "+version)}
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			f.hits.Add(1)
			fmt.Fprintf(w, `{"tag_name":%q,"html_url":"x","assets":[
				{"name":%q,"browser_download_url":"%s/dl/%s"},
				{"name":"caveira_plan9_mips.tar.gz","browser_download_url":"%s/dl/other"},
				{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt"}]}`,
				f.version, name, f.URL, name, f.URL, f.URL)
		case "/dl/" + name:
			w.Write(f.asset)
		case "/dl/checksums.txt":
			s := sum(f.asset)
			if f.corrupt {
				s = sum([]byte("something else"))
			}
			fmt.Fprintf(w, "%s  %s\n%s  caveira.dmg\n", s, name, sum([]byte("dmg")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeReleases) client(t *testing.T) *Client {
	return &Client{
		Latest:      f.URL + "/latest",
		Cache:       filepath.Join(t.TempDir(), "update.json"),
		CheckBinary: func(context.Context, string, string) error { return nil },
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.3.1", "v0.3.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"0.3.0", "v0.2.0", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.2.9", "v0.3.0", false},
		{"v0.3.0", "dev", false},
		{"v0.3.0", "v0.2.0-4-gabc1234", false},
		{"v0.3.0", "v0.2.0-dirty", false},
		{"garbage", "v0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.3.0": true, "0.3.0": true, "dev": false, "": false, "abc1234": false,
		"v0.3.0-1-gdeadbee": false, "v0.3.0-dirty": false,
	} {
		if IsRelease(v) != want {
			t.Errorf("IsRelease(%q) = %v", v, !want)
		}
	}
}

func TestAssetChoice(t *testing.T) {
	for _, c := range [][3]string{
		{"darwin", "arm64", "caveira_darwin_arm64.tar.gz"},
		{"darwin", "amd64", "caveira_darwin_amd64.tar.gz"},
		{"linux", "amd64", "caveira_linux_amd64.tar.gz"},
		{"linux", "arm64", "caveira_linux_arm64.tar.gz"},
	} {
		if got := AssetName(c[0], c[1]); got != c[2] {
			t.Errorf("AssetName(%s, %s) = %s", c[0], c[1], got)
		}
	}
	rel := Release{Assets: []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{{"caveira_linux_amd64.tar.gz", "L"}, {"caveira_darwin_arm64.tar.gz", "D"}}}
	if rel.Asset("caveira_darwin_arm64.tar.gz") != "D" || rel.Asset("nope") != "" {
		t.Fatal("Asset picks the wrong file")
	}
}

func TestCheckCachesForADay(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("CAVEIRA_NO_UPDATE_CHECK", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	f := newFake(t, "v0.3.1")
	c := f.client(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	ctx := context.Background()

	got, err := c.Check(ctx, "v0.3.0", Day)
	if err != nil || got != "v0.3.1" {
		t.Fatalf("Check = %q, %v", got, err)
	}
	now = now.Add(23 * time.Hour)
	if got, _ := c.Check(ctx, "v0.3.0", Day); got != "v0.3.1" || f.hits.Load() != 1 {
		t.Fatalf("second check within a day asked GitHub again (%d) or forgot (%q)", f.hits.Load(), got)
	}
	f.version = "v0.4.0"
	now = now.Add(2 * time.Hour)
	if got, _ := c.Check(ctx, "v0.3.0", Day); got != "v0.4.0" || f.hits.Load() != 2 {
		t.Fatalf("after a day: %q, %d hits", got, f.hits.Load())
	}
	if got, _ := c.Check(ctx, "v0.4.0", Day); got != "" {
		t.Fatalf("up to date still offered %q", got)
	}
}

func TestCheckQuietForDevAndWhenOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	f := newFake(t, "v9.0.0")
	c := f.client(t)
	for _, v := range []string{"dev", "v0.2.0-3-gabcdef0", "abcdef0"} {
		if got, err := c.Check(context.Background(), v, Day); got != "" || err != nil {
			t.Errorf("dev build %q nagged: %q %v", v, got, err)
		}
	}
	t.Setenv("CAVEIRA_NO_UPDATE_CHECK", "1")
	if got, _ := c.Check(context.Background(), "v0.1.0", Day); got != "" {
		t.Errorf("CAVEIRA_NO_UPDATE_CHECK ignored")
	}
	if f.hits.Load() != 0 {
		t.Errorf("asked GitHub %d times", f.hits.Load())
	}

	t.Setenv("CAVEIRA_NO_UPDATE_CHECK", "")
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".caveira"), 0o700)
	os.WriteFile(filepath.Join(home, ".caveira", "config.json"), []byte(`{"no_update_check": true}`), 0o600)
	if !Disabled() {
		t.Fatal("no_update_check in config.json ignored")
	}
}

func TestReplaceBinaryKeepsLinkWorking(t *testing.T) {
	skipOnWindows(t)
	f := newFake(t, "v0.3.1")
	c := f.client(t)
	rel, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "caveira")
	os.WriteFile(bin, []byte("old"), 0o755)
	os.Symlink("caveira", filepath.Join(dir, "cav"))

	// Called through the link, as `cav update` would be.
	if err := c.ReplaceBinary(context.Background(), rel, filepath.Join(dir, "cav")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "cav"))
	if string(b) != "#!new binary v0.3.1" {
		t.Fatalf("cav reads %q", b)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "cav")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("cav is no longer a link")
	}
	if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".caveira-update-*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestChecksumMismatchRefuses(t *testing.T) {
	skipOnWindows(t)
	f := newFake(t, "v0.3.1")
	f.corrupt = true
	c := f.client(t)
	rel, _ := c.Fetch(context.Background())
	dir := t.TempDir()
	bin := filepath.Join(dir, "caveira")
	os.WriteFile(bin, []byte("old"), 0o755)

	err := c.ReplaceBinary(context.Background(), rel, bin)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary was touched: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestNotWritableSaysWhatToRun(t *testing.T) {
	skipOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	f := newFake(t, "v0.3.1")
	c := f.client(t)
	rel, _ := c.Fetch(context.Background())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "caveira")
	os.WriteFile(bin, []byte("old"), 0o755)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)

	err := c.ReplaceBinary(context.Background(), rel, bin)
	var nw *NotWritableError
	if !errors.As(err, &nw) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(nw.Error(), "sudo env CAVEIRA_INSTALL_DIR="+dir+" sh") {
		t.Fatalf("message: %s", nw.Error())
	}
}

func TestBundle(t *testing.T) {
	skipOnWindows(t)
	if got := Bundle("/Applications/caveira.app/Contents/MacOS/caveira"); got != "/Applications/caveira.app" {
		t.Errorf("Bundle = %q", got)
	}
	if got := Bundle("/Users/x/repo/desktop/build/bin/caveira"); got != "" {
		t.Errorf("Bundle outside an app = %q", got)
	}
}

func TestReplaceApp(t *testing.T) {
	skipOnWindows(t)
	if runtime.GOOS != "darwin" {
		t.Skip("ditto is macOS's")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "caveira.app")
	dst := filepath.Join(dir, "Applications", "caveira.app")
	os.MkdirAll(filepath.Join(src, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(src, "Contents", "MacOS", "caveira"), []byte("new"), 0o755)
	os.MkdirAll(filepath.Join(dst, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(dst, "Contents", "MacOS", "caveira"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(dst, "Contents", "stale"), []byte("x"), 0o644)

	if err := ReplaceApp(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Contents", "MacOS", "caveira")); string(b) != "new" {
		t.Fatalf("app holds %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "Contents", "stale")); err == nil {
		t.Fatal("old files survived")
	}
	if b, _ := os.ReadFile(filepath.Join(AppBackup(dst), "Contents", "stale")); len(b) == 0 {
		t.Fatal("the old app was not kept")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "Applications", ".*"))
	if len(left) != 1 || left[0] != AppBackup(dst) {
		t.Fatalf("left behind %v", left)
	}
}
