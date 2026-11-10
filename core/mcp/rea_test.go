package mcp

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// reaFake is a stand-in GitHub and npm registry with rea's releases on
// them. A package whose bytes say "broken" installs, and fails to start.
type reaFake struct {
	latest      string
	commits     map[string]string // what each release tag points at
	builtFrom   map[string]string // what the provenance says it was built from
	pkgs        map[string]string
	integrity   map[string]string // what the registry claims, when not the truth
	unverified  map[string]bool   // provenance signed by someone else
	tarballHost string
	gh, reg     *httptest.Server
	requests    atomic.Int32
	notModified atomic.Int32
}

func newReaFake(t *testing.T, latest string, versions ...string) *reaFake {
	f := &reaFake{latest: latest, commits: map[string]string{}, builtFrom: map[string]string{}, pkgs: map[string]string{},
		integrity: map[string]string{}, unverified: map[string]bool{}}
	for i, v := range versions {
		f.add(v, "rea "+v, strings.Repeat(string(rune('a'+i)), 40))
	}
	f.gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		etag := `"e-` + f.latest + `"`
		switch p := r.URL.Path; {
		case p == "/repos/morluto/rea/releases/latest":
			if r.Header.Get("If-None-Match") == etag {
				f.notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			json.NewEncoder(w).Encode(map[string]any{"tag_name": reaTagPrefix + f.latest})
		case strings.HasPrefix(p, "/repos/morluto/rea/git/ref/tags/"+reaTagPrefix):
			v := strings.TrimPrefix(p, "/repos/morluto/rea/git/ref/tags/"+reaTagPrefix)
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": f.commits[v], "type": "commit"}})
		default:
			http.NotFound(w, r)
		}
	}))
	f.reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/rea-agents/-/rea-agents-"):
			v := strings.TrimSuffix(strings.TrimPrefix(p, "/rea-agents/-/rea-agents-"), ".tgz")
			w.Write([]byte(f.pkgs[v]))
		case strings.HasPrefix(p, "/-/npm/v1/attestations/rea-agents@"):
			v := strings.TrimPrefix(p, "/-/npm/v1/attestations/rea-agents@")
			json.NewEncoder(w).Encode(map[string]any{"attestations": []any{
				map[string]any{"predicateType": slsaV1, "bundle": map[string]string{"about": v}},
			}})
		case strings.HasPrefix(p, "/rea-agents/"):
			v := strings.TrimPrefix(p, "/rea-agents/")
			if _, ok := f.pkgs[v]; !ok {
				http.NotFound(w, r)
				return
			}
			host := f.reg.URL
			if f.tarballHost != "" {
				host = f.tarballHost
			}
			integrity := f.integrity[v]
			if integrity == "" {
				sum := sha512.Sum512([]byte(f.pkgs[v]))
				integrity = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
			}
			json.NewEncoder(w).Encode(map[string]any{"version": v, "dist": map[string]any{
				"tarball":      host + "/rea-agents/-/rea-agents-" + v + ".tgz",
				"integrity":    integrity,
				"attestations": map[string]string{"url": f.reg.URL + "/-/npm/v1/attestations/rea-agents@" + v},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.gh.Close)
	t.Cleanup(f.reg.Close)
	return f
}

func (f *reaFake) add(v, body, commit string) {
	f.pkgs[v], f.commits[v], f.builtFrom[v] = body, commit, commit
}

func (f *reaFake) source() reaSource {
	return reaSource{
		GitHub: f.gh.URL, Registry: f.reg.URL, HTTP: &http.Client{Timeout: 10 * time.Second},
		Verify: func(bundle []byte, v string, sum []byte, commit string) error {
			var b struct{ About string }
			json.Unmarshal(bundle, &b)
			want := sha512.Sum512([]byte(f.pkgs[v]))
			switch {
			case f.unverified[v]:
				return unverified{errors.New("signed by another workflow")}
			case b.About != v || commit != f.builtFrom[v] || !reflect.DeepEqual(sum, want[:]):
				return unverified{errors.New("not this package")}
			}
			return nil
		},
		Install: func(_ context.Context, dir, tarball, v string) error {
			b, err := os.ReadFile(tarball)
			if err != nil {
				return err
			}
			scripts := filepath.Join(dir, "node_modules", reaPackage, "scripts")
			if err := os.MkdirAll(scripts, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(scripts, "rea.mjs"), b, 0o600)
		},
		Check: func(_ context.Context, v string) error {
			b, err := os.ReadFile(reaScript(v))
			if err != nil || strings.Contains(string(b), "broken") {
				return errors.New("exited with status 1: rea: this build is broken")
			}
			return nil
		},
	}
}

func writeRea(t *testing.T, s Server) {
	t.Helper()
	if err := WriteFile(UserPath(), File{Servers: map[string]Server{"rea": s}}); err != nil {
		t.Fatal(err)
	}
}

func readRea(t *testing.T) Server {
	t.Helper()
	f, err := ReadFile(UserPath())
	if err != nil {
		t.Fatal(err)
	}
	return f.Servers["rea"]
}

var seeded670 = Server{Command: "npx", Args: []string{"-y", "rea-agents@6.7.0", "mcp"}}

func mustUpdate(t *testing.T, src reaSource, pin, want string) {
	t.Helper()
	got, err := src.update(context.Background(), pin)
	if err != nil || got != want {
		t.Fatalf("update(%q) = %q, %v; want %q", pin, got, err, want)
	}
}

func TestReaUpdatesTheSeededEntryToAVerifiedRelease(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.7.0", "6.8.0")
	writeRea(t, seeded670)

	mustUpdate(t, f.source(), "", "installed rea 6.8.0")
	st := loadReaState()
	if st.Current != "6.8.0" || ReaVersion() != "6.8.0" {
		t.Fatalf("state %+v", st)
	}
	sum := sha512.Sum512([]byte("rea 6.8.0"))
	if p := st.Pins["6.8.0"]; p.Tag != "rea-agents-6.8.0" || p.Commit != f.commits["6.8.0"] || p.SHA512 != hex.EncodeToString(sum[:]) {
		t.Fatalf("pin %+v", p)
	}
	if got := readRea(t); !reflect.DeepEqual(got, ReaServer) {
		t.Fatalf("mcp.json entry %+v, want the managed one", got)
	}

	// The next launch asks GitHub with the ETag and gets 304: nothing else
	// is fetched.
	before := f.requests.Load()
	mustUpdate(t, f.source(), "", "rea 6.8.0 is the newest release")
	if f.notModified.Load() != 1 || f.requests.Load() != before+1 {
		t.Fatalf("304s %d, requests %d", f.notModified.Load(), f.requests.Load()-before)
	}
}

func TestReaKeepsTheVersionBefore(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.7.0", "6.8.0", "6.9.0")
	writeRea(t, seeded670)
	mustUpdate(t, f.source(), "6.7.0", "installed rea 6.7.0")
	mustUpdate(t, f.source(), "", "updated rea from 6.7.0 to 6.8.0")
	st := loadReaState()
	if st.Current != "6.8.0" || st.Previous != "6.7.0" || !reaInstalled("6.7.0") {
		t.Fatalf("state %+v", st)
	}
	f.latest = "6.9.0"
	mustUpdate(t, f.source(), "", "updated rea from 6.8.0 to 6.9.0")
	st = loadReaState()
	if st.Current != "6.9.0" || st.Previous != "6.8.0" {
		t.Fatalf("state %+v", st)
	}
	// 6.7.0 is retired, and stays on disk a few days for a session that
	// may still be running it.
	if _, ok := st.Retired["6.7.0"]; !ok || !reaInstalled("6.7.0") {
		t.Fatalf("6.7.0 should be retired and still there: %+v", st)
	}
}

func TestReaRefusesWhatDoesNotVerify(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *reaFake)
		err   string
		bad   bool
	}{
		{"provenance from someone else", func(f *reaFake) { f.unverified["6.8.0"] = true }, "not installed: signed by another workflow", true},
		{"a package built from another commit than the tag's", func(f *reaFake) { f.builtFrom["6.8.0"] = strings.Repeat("f", 40) }, "not installed: not this package", true},
		{"a download that doesn't match the registry", func(f *reaFake) {
			f.integrity["6.8.0"] = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
		}, "doesn't match the registry's SHA-512", false},
		{"a tarball somewhere else", func(f *reaFake) { f.tarballHost = "https://elsewhere.example" }, "is not on the registry", false},
		{"a package changed after it was verified", func(f *reaFake) {
			st := reaState{Pins: map[string]reaPin{"6.8.0": {SHA512: "00", Verified: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}}}
			if err := st.save(); err != nil {
				panic(err)
			}
		}, "not the one verified on 2026-10-25; refused", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home(t)
			f := newReaFake(t, "6.8.0", "6.8.0")
			writeRea(t, seeded670)
			c.setup(f)
			_, err := f.source().update(context.Background(), "")
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err %v, want %q", err, c.err)
			}
			if ReaVersion() != "" || reaInstalled("6.8.0") {
				t.Fatal("nothing should be installed")
			}
			if got := readRea(t); !reflect.DeepEqual(got, seeded670) {
				t.Fatalf("the npx entry should be left as it was: %+v", got)
			}
			st := loadReaState()
			if _, bad := st.Bad["6.8.0"]; bad != c.bad {
				t.Fatalf("bad %v: %+v", bad, st.Bad)
			}
		})
	}
}

func TestReaSkipsAVersionThatFailedToVerify(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.8.0")
	writeRea(t, seeded670)
	f.unverified["6.8.0"] = true
	if _, err := f.source().update(context.Background(), ""); err == nil {
		t.Fatal("should be refused")
	}
	before := f.requests.Load()
	mustUpdate(t, f.source(), "", "rea 6.8.0 is skipped (its provenance didn't verify); waiting for a newer release")
	if f.requests.Load() != before+1 {
		t.Fatal("a skipped version is not downloaded again")
	}
}

func TestReaANewVersionThatWontStartIsNotUsed(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.7.0")
	f.add("6.8.0", "rea broken 6.8.0", strings.Repeat("9", 40))
	writeRea(t, seeded670)
	mustUpdate(t, f.source(), "6.7.0", "installed rea 6.7.0")
	_, err := f.source().update(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "installed but didn't start, so it isn't used") {
		t.Fatalf("err %v", err)
	}
	st := loadReaState()
	if st.Current != "6.7.0" || reaInstalled("6.8.0") || !strings.Contains(st.Bad["6.8.0"], "didn't start") {
		t.Fatalf("state %+v", st)
	}
	if st.Checked == nil || !strings.Contains(st.Checked.Result, "didn't start") {
		t.Fatalf("the launch's result should be on record: %+v", st.Checked)
	}
}

// managedTestRea is the managed entry, run by the test binary standing in
// for node: the script it is given decides whether it starts.
func managedTestRea() Server {
	return Server{Command: os.Args[0], Args: []string{"${" + ReaVar + "}", "mcp"}, Env: map[string]string{"CAVEIRA_TEST_MCP_SERVER": "1"}}
}

func reaStatus(t *testing.T, m *Manager) State {
	t.Helper()
	m.Wait(context.Background(), 30*time.Second)
	for _, s := range m.States() {
		if s.Name == "rea" {
			return s
		}
	}
	t.Fatal("no rea")
	return State{}
}

func TestReaRollsBackANewVersionThatFailsInASession(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.7.0", "6.8.0")
	writeRea(t, seeded670)
	mustUpdate(t, f.source(), "6.7.0", "installed rea 6.7.0")
	mustUpdate(t, f.source(), "", "updated rea from 6.7.0 to 6.8.0")
	// 6.8.0 passed its check, and breaks in a real session.
	if err := os.WriteFile(reaScript("6.8.0"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRea(t, managedTestRea())

	m := NewManager(project(t), "test")
	m.Start()
	t.Cleanup(m.Close)
	s := reaStatus(t, m)
	if s.Status != Ready || len(s.Tools) == 0 {
		t.Fatalf("rea should be running on 6.7.0: %+v", s)
	}
	if s.Summary != "rea-agents 6.7.0 · 6.8.0 didn't start, so caveira went back" {
		t.Fatalf("summary %q", s.Summary)
	}
	st := loadReaState()
	if st.Current != "6.7.0" || st.Previous != "" || reaInstalled("6.8.0") || st.Rollback == nil || st.Rollback.From != "6.8.0" ||
		!strings.Contains(st.Bad["6.8.0"], "this build is broken") || !reaHealthy("6.7.0") {
		t.Fatalf("state %+v", st)
	}
	// The next launch doesn't put 6.8.0 back.
	got, err := f.source().update(context.Background(), "")
	if err != nil || !strings.HasPrefix(got, "rea 6.8.0 is skipped (failed to start: ") {
		t.Fatalf("%q, %v", got, err)
	}
	if st := loadReaState(); st.Current != "6.7.0" || reaInstalled("6.8.0") {
		t.Fatalf("state %+v", st)
	}
}

func TestReaKeepsAVersionThatHasStartedBefore(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.7.0", "6.8.0")
	writeRea(t, seeded670)
	mustUpdate(t, f.source(), "6.7.0", "installed rea 6.7.0")
	mustUpdate(t, f.source(), "", "updated rea from 6.7.0 to 6.8.0")
	writeRea(t, managedTestRea())
	m := NewManager(project(t), "test")
	m.Start()
	if s := reaStatus(t, m); s.Status != Ready {
		t.Fatalf("%+v", s)
	}
	m.Close()
	// Now it breaks (a missing runtime, say): that is not the release's
	// fault, so it stays, and says why it can't start.
	os.WriteFile(reaScript("6.8.0"), []byte("broken"), 0o600)
	m = NewManager(project(t), "test")
	m.Start()
	t.Cleanup(m.Close)
	if s := reaStatus(t, m); s.Status != Failed || !strings.Contains(s.Error, "broken") {
		t.Fatalf("%+v", s)
	}
	if st := loadReaState(); st.Current != "6.8.0" || st.Previous != "6.7.0" {
		t.Fatalf("state %+v", st)
	}
}

func TestReaLeavesItsEntryAlone(t *testing.T) {
	cases := []struct {
		name  string
		entry *Server
		env   string
		want  string
	}{
		{"changed by hand", &Server{Command: "npx", Args: []string{"-y", "rea-agents@6.7.0", "mcp", "--verbose"}}, "",
			"rea's entry in mcp.json was changed by hand, so caveira leaves it alone"},
		{"turned off", &Server{Command: "npx", Args: []string{"-y", "rea-agents@6.7.0", "mcp"}, Disabled: true}, "", "rea is turned off"},
		{"removed", nil, "", "rea isn't set up in mcp.json"},
		{"no updating at launch", &seeded670, "CAVEIRA_NO_AUTO_UPDATE", "updating at launch is off"},
		{"no update checks", &seeded670, "CAVEIRA_NO_UPDATE_CHECK", "updating at launch is off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home(t)
			if c.env != "" {
				t.Setenv(c.env, "1")
			}
			f := newReaFake(t, "6.8.0", "6.8.0")
			if c.entry != nil {
				writeRea(t, *c.entry)
			} else {
				writeJSON(t, UserPath(), `{"mcpServers":{}}`)
			}
			mustUpdate(t, f.source(), "", c.want)
			if f.requests.Load() != 0 {
				t.Fatalf("%d requests", f.requests.Load())
			}
		})
	}
}

func TestReaAnInterruptedInstallIsCleanedUp(t *testing.T) {
	home(t)
	f := newReaFake(t, "6.8.0", "6.8.0")
	writeRea(t, seeded670)
	left := filepath.Join(reaVersionsDir(), "6.8.0.partial-123")
	os.MkdirAll(left, 0o700)
	// A lock from a caveira that died long ago is taken over.
	lock := filepath.Join(ReaDir(), ".lock")
	os.WriteFile(lock, nil, 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(lock, old, old)
	mustUpdate(t, f.source(), "", "installed rea 6.8.0")
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatal("the half-done install should be gone")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("the lock should be let go")
	}
}

func TestReaVersionsCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"6.10.0", "6.9.0", true}, {"6.8.0", "6.8.0", false}, {"6.7.0", "6.8.0", false}, {"6.8.0", "", true}, {"6.9.0-rc.1", "6.8.0", false}, {"06.9.0", "6.8.0", false}} {
		if got := reaNewer(c.a, c.b); got != c.want {
			t.Errorf("reaNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// The real check, on rea 6.8.0's real provenance and Sigstore's real
// keys, saved in testdata so it runs offline.
func TestReaProvenanceOfARealRelease(t *testing.T) {
	trusted, err := root.NewTrustedRootFromPath("testdata/sigstore-trusted-root.json")
	if err != nil {
		t.Fatal(err)
	}
	att, err := os.ReadFile("testdata/rea-6.8.0-attestations.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := reaProvenanceBundle(att)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := hex.DecodeString("f500de4671aa103cc0059cabd3878b9c15c1bff73a23dfd35aeffa334303e2911f932488fc1220c1338321b011797dc2d71ea18f0f7e265c78036c9448f67dc3")
	const commit = "160d821ac9a0310afcb8d1fc427862dd6508add5"    // rea-agents-6.8.0
	const commit670 = "27dc42d10249f0410904a75c0997842eb03b931c" // rea-agents-6.7.0
	if err := verifyReaProvenance(trusted, bundle, "6.8.0", sum, commit); err != nil {
		t.Fatalf("the real release should verify: %v", err)
	}
	other := append([]byte(nil), sum...)
	other[0] ^= 1
	var b map[string]any
	json.Unmarshal(bundle, &b)
	env := b["dsseEnvelope"].(map[string]any)
	payload, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
	env["payload"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "morluto/rea", "morluto/reb", 1)))
	edited, _ := json.Marshal(b)
	for name, c := range map[string]struct {
		bundle  []byte
		version string
		sum     []byte
		commit  string
	}{
		"another commit than the tag's": {bundle, "6.8.0", sum, commit670},
		"another package":               {bundle, "6.8.0", other, commit},
		"another version":               {bundle, "6.9.0", sum, commit},
		"an edited statement":           {edited, "6.8.0", sum, commit},
	} {
		if err := verifyReaProvenance(trusted, c.bundle, c.version, c.sum, c.commit); err == nil {
			t.Errorf("%s: verified", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
	for branch, want := range map[string]bool{"main": true, "release/6.8.0": true, "release/6.7.0": false, "evil": false, "main-evil": false} {
		if got := regexp.MustCompile(reaSigner("6.8.0")).MatchString(reaWorkflow + "@refs/heads/" + branch); got != want {
			t.Errorf("a build from %s accepted: %v", branch, got)
		}
	}
	if _, err := npmSHA512("sha512-" + base64.StdEncoding.EncodeToString(sum)); err != nil {
		t.Fatal(err)
	}
	if _, err := npmSHA512("sha1-abc"); err == nil {
		t.Fatal("a sha1 integrity should not do")
	}
}
