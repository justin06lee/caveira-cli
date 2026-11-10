package ui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	hexenc "encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/justin06lee/caveira/core/update"
)

// fakeRelease serves version as the latest release, with a terminal
// client for this platform that says "new".
func fakeRelease(t *testing.T, version string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "caveira", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("new"))
	tw.Close()
	gz.Close()
	tgz := buf.Bytes()
	h := sha256.Sum256(tgz)
	name := update.AssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":"%s/a"},{"name":"checksums.txt","browser_download_url":"%s/sums"}]}`, version, name, srv.URL, srv.URL)
		case "/a":
			w.Write(tgz)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hexenc.EncodeToString(h[:]), name)
		}
	}))
	t.Cleanup(srv.Close)
	old := updates
	updates = &update.Client{
		Latest:      srv.URL + "/latest",
		Cache:       filepath.Join(t.TempDir(), "u.json"),
		CheckBinary: func(context.Context, string, string) error { return nil },
	}
	t.Cleanup(func() { updates = old })
}

func TestUpdateReplacesTheBinaryAndSaysRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("caveira does not update itself on Windows; core/update has the test")
	}
	fakeRelease(t, "v0.5.0")
	bin := filepath.Join(t.TempDir(), "caveira")
	os.WriteFile(bin, []byte("old"), 0o755)
	oldExe := executable
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = oldExe })

	m := testModel(t, 100, 30)
	m.phase = phaseSession
	cmd := m.slash("/update")
	m.updateDone(cmd().(updateDoneMsg))
	last := m.items[len(m.items)-1]
	if last.kind != itemNotice || !strings.Contains(last.text, "Updated to caveira v0.5.0") || !strings.Contains(last.text, "restart") {
		t.Fatalf("last item: %v %q", last.kind, last.text)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new" {
		t.Fatalf("binary holds %q", b)
	}
}

func TestUpdateOnALatestOrDevBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("caveira does not update itself on Windows; core/update has the test")
	}
	fakeRelease(t, "v0.4.0")
	if text, err := SelfUpdate(context.Background(), "v0.4.0", func(string) {}); err != nil || !strings.Contains(text, "is the latest") {
		t.Fatalf("%q %v", text, err)
	}
	if _, err := SelfUpdate(context.Background(), "v0.3.0-2-gabc1234", func(string) {}); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("dev build: %v", err)
	}
}

func TestUpdateLineOnHomeAndInTranscript(t *testing.T) {
	m := testModel(t, 100, 40)
	m.updateAvailable(updateAvailableMsg{version: "v0.5.0"})
	frame, _ := m.renderHome(0)
	if !strings.Contains(plain(frame), "caveira v0.5.0 · run /update") {
		t.Fatal("home screen does not say a release is out")
	}
	m.pushHeader()
	if len(m.items) != 2 || m.items[1].text != updateLine("v0.5.0") {
		t.Fatalf("transcript opens without the update line: %d items", len(m.items))
	}
}
