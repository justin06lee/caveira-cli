package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/mcp"
)

// isolate gives the test a home and an environment of its own, signing in
// through site: nothing touches the real ~/.caveira or uses the
// developer's keys.
func isolate(t *testing.T, site string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	// run() goes into the folder -C names, and Windows cannot remove a folder
	// a process is in: this puts the working directory back before the temp
	// folders go.
	t.Chdir(os.Getenv("HOME"))
	// The built-in servers count as offered already, so run() starts no npx,
	// whose process would also hold those folders open.
	var names []string
	for _, d := range mcp.Defaults {
		names = append(names, d.Name)
	}
	offered, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), "mcp-defaults.json"), offered, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEIRA_CLOUD_URL", site)
	for _, k := range []string{"CAVEIRA_API_KEY", "CAVEIRA_BASE_URL", "CAVEIRA_MODEL"} {
		t.Setenv(k, "")
	}
}

// site stands in for the caveira site. /api/me takes only the token the
// login hands out.
func site(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logouts atomic.Int32
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("POST /api/auth/device/start", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc", "user_code": "WXYZ-1234",
			"verification_uri_complete": srv.URL + "/device?code=WXYZ-1234",
			"interval":                  1, "expires_in": 600,
		})
	})
	mux.HandleFunc("POST /api/auth/device/poll", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"tok","email":"rebel@example.dev"}`))
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"Sign in again","type":"caveira","code":"unauthorized"}}`))
			return
		}
		w.Write([]byte(`{"email":"rebel@example.dev","plan":{"id":"free","name":"Free"}}`))
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		logouts.Add(1)
		w.Write([]byte(`{}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &logouts
}

// caveira login prints the page and the code, opens the browser, and says
// who it signed in as; run again it says so, unless the site has stopped
// taking the token. caveira logout tells the site and forgets it here.
func TestLoginAndLogoutCommands(t *testing.T) {
	srv, logouts := site(t)
	isolate(t, srv.URL)

	var out bytes.Buffer
	var opened []string
	open := func(u string) error { opened = append(opened, u); return nil }
	if err := login(&out, open); err != nil {
		t.Fatal(err)
	}
	page := srv.URL + "/device?code=WXYZ-1234"
	for _, want := range []string{"Opening your browser", page, "WXYZ-1234", "Signed in as rebel@example.dev"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("login printed\n%s\nmissing %q", out.String(), want)
		}
	}
	if len(opened) != 1 || opened[0] != page {
		t.Fatalf("browser opened at %v, want %s", opened, page)
	}
	if a, ok := cloud.Load(); !ok || a.Token != "tok" {
		t.Fatal("login kept no account")
	}

	out.Reset()
	if err := login(&out, open); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Already signed in as rebel@example.dev") || len(opened) != 1 {
		t.Fatalf("login while signed in printed %q", out.String())
	}

	// A token the site no longer takes is replaced, and where the browser
	// cannot open, the page is still there to visit.
	cloud.Save(cloud.Auth{URL: srv.URL, Token: "stale", Email: "rebel@example.dev"})
	out.Reset()
	if err := login(&out, func(string) error { return os.ErrNotExist }); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "To sign in to caveira, visit\n\n  "+page) || !strings.Contains(out.String(), "Signed in as") {
		t.Fatalf("login over a rejected token printed\n%s", out.String())
	}

	out.Reset()
	if err := logout(&out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Signed out of rebel@example.dev\n" || logouts.Load() != 1 {
		t.Fatalf("logout printed %q, site told %d times", out.String(), logouts.Load())
	}
	if _, ok := cloud.Load(); ok {
		t.Fatal("logout kept the account")
	}
	out.Reset()
	logout(&out)
	if out.String() != "Not signed in.\n" {
		t.Fatalf("logout signed out printed %q", out.String())
	}
}

// Signed in, an endpoint given with --base-url gets the request as given,
// never the account's token.
func TestBaseURLFlagNeverGetsTheAccountToken(t *testing.T) {
	srv, _ := site(t)
	isolate(t, srv.URL)
	cloud.Save(cloud.Auth{URL: srv.URL, Token: "tok", Email: "rebel@example.dev"})
	var sent atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(other.Close)

	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"caveira", "-C", t.TempDir(), "--base-url", "http://localhost:" + other.URL[len("http://127.0.0.1:"):], "-p", "hi"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := sent.Load().(string); got != "" {
		t.Fatalf("--base-url got Authorization %q", got)
	}

	// The local server's own key does, from the environment or --api-key.
	t.Setenv("CAVEIRA_API_KEY", "ck_own")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := sent.Load().(string); got != "Bearer ck_own" {
		t.Fatalf("--base-url with CAVEIRA_API_KEY got Authorization %q", got)
	}
	os.Args = []string{"caveira", "-C", t.TempDir(), "--base-url", "http://localhost:" + other.URL[len("http://127.0.0.1:"):], "--api-key", "flag-key", "-p", "hi"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := sent.Load().(string); got != "Bearer flag-key" {
		t.Fatalf("--api-key got Authorization %q", got)
	}
	t.Setenv("CAVEIRA_API_KEY", "")
	os.Args = []string{"caveira", "-C", t.TempDir(), "--base-url", "http://localhost:" + other.URL[len("http://127.0.0.1:"):], "-p", "hi"}

	// Signed out, a local server runs on its own.
	os.Remove(cloud.Path())
	sent.Store("unset")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := sent.Load().(string); got != "" {
		t.Fatalf("signed out, --base-url got Authorization %q", got)
	}
}

// A hosted endpoint is refused before anything is sent, in one line, and
// the usage offers no key.
func TestHostedBaseURLIsRefused(t *testing.T) {
	srv, _ := site(t)
	isolate(t, srv.URL)
	cloud.Save(cloud.Auth{URL: srv.URL, Token: "tok", Email: "rebel@example.dev"})
	args := os.Args
	t.Cleanup(func() { os.Args = args })

	os.Args = []string{"caveira", "-C", t.TempDir(), "--base-url", "https://api.openai.com/v1", "-p", "hi"}
	err := run()
	if err == nil || err.Error() != "--base-url: api.openai.com is not on this machine or your network: caveira only runs local models (localhost, 10.x, 172.16-31.x, 192.168.x or a .local name), or hosted ones through your caveira account" {
		t.Fatalf("hosted --base-url: %v", err)
	}

	t.Setenv("CAVEIRA_BASE_URL", "https://openrouter.ai/api/v1")
	os.Args = []string{"caveira", "-C", t.TempDir(), "-p", "hi"}
	if err := run(); err == nil || !strings.HasPrefix(err.Error(), "CAVEIRA_BASE_URL: openrouter.ai is not on this machine") {
		t.Fatalf("hosted CAVEIRA_BASE_URL: %v", err)
	}

	// A key never goes to a hosted endpoint, nor to the account: alone,
	// --api-key is refused.
	t.Setenv("CAVEIRA_BASE_URL", "")
	os.Args = []string{"caveira", "-C", t.TempDir(), "--api-key", "k", "-p", "hi"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "--api-key is for a local model server") {
		t.Fatalf("--api-key without --base-url: %v", err)
	}
	os.Args = []string{"caveira", "-C", t.TempDir(), "--base-url", "https://api.openai.com/v1", "--api-key", "k", "-p", "hi"}
	if err := run(); err == nil || !strings.HasPrefix(err.Error(), "--base-url: api.openai.com is not on this machine") {
		t.Fatalf("hosted --base-url with --api-key: %v", err)
	}
}

// -p with no account and no key fails at once and says to sign in.
func TestPrintModeSignedOutSaysToLogin(t *testing.T) {
	srv, _ := site(t)
	isolate(t, srv.URL)
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"caveira", "-C", t.TempDir(), "-p", "explain main.go"}
	err := run()
	if err == nil || !strings.Contains(err.Error(), `Run "caveira login"`) {
		t.Fatalf("print mode signed out: %v", err)
	}
}
