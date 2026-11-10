package mcp

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/proc"
	"github.com/justin06lee/caveira/core/update"
)

// reaSource is where a launch looks for rea: GitHub for the newest
// release and the commit its tag points at, npm for the package built from
// it and that build's provenance. Tests point it at stand-ins.
type reaSource struct {
	GitHub   string
	Registry string
	HTTP     *http.Client
	// Verify checks a package's provenance (reaverify.go); Install puts a
	// verified package and its dependencies in a folder (npmInstall);
	// Check starts an installed version once (checkRea).
	Verify  func(bundle []byte, version string, sha512 []byte, commit string) error
	Install func(ctx context.Context, dir, tarball, version string) error
	Check   func(ctx context.Context, version string) error
	// Needs are the programs installing and running rea takes.
	Needs []string
}

// The public registry, whatever a user's .npmrc says: rea's own package
// is checked against it, and its dependencies come from it too.
const npmRegistry = "https://registry.npmjs.org"

func defaultReaSource() reaSource {
	return reaSource{
		GitHub:   "https://api.github.com",
		Registry: npmRegistry,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
		Verify: func(bundle []byte, version string, sha512 []byte, commit string) error {
			trusted, err := reaTrustedRoot()
			if err != nil {
				return err // can't check now: try again next launch
			}
			if err := verifyReaProvenance(trusted, bundle, version, sha512, commit); err != nil {
				return unverified{err}
			}
			return nil
		},
		Install: npmInstall,
		Check:   checkRea,
		Needs:   []string{"node", "npm"},
	}
}

// unverified is a package that was checked and failed, as opposed to
// one that couldn't be checked; only the first marks a version bad.
type unverified struct{ error }

func (u unverified) Unwrap() error { return u.error }

// UpdateRea is a launch's look for a newer rea: when there is one, and
// it verifies, installs and starts, sessions from the next launch on use
// it. A session already running keeps the version it started. It returns
// what it did, in one line; everything that goes wrong leaves the version
// in use as it was. Run it in the background, on a context that ends
// with the process.
func UpdateRea(ctx context.Context) (string, error) {
	return defaultReaSource().update(ctx, "")
}

// InstallRea installs version v the way UpdateRea installs the newest
// release, verified the same way. It is for tests and for putting a
// version back by hand.
func InstallRea(ctx context.Context, v string) (string, error) {
	return defaultReaSource().update(ctx, v)
}

func (src reaSource) update(ctx context.Context, pin string) (result string, err error) {
	if update.OptedOut() {
		return "updating at launch is off", nil
	}
	f, ferr := ReadFile(UserPath())
	if ferr != nil {
		return "", ferr
	}
	entry, ok := f.Servers["rea"]
	switch {
	case !ok:
		return "rea isn't set up in mcp.json", nil
	case entry.Disabled:
		return "rea is turned off", nil
	case !seededRea(entry) && !managedRea(entry):
		return "rea's entry in mcp.json was changed by hand, so caveira leaves it alone", nil
	}
	for _, tool := range src.Needs {
		if _, err := exec.LookPath(tool); err != nil {
			return "", fmt.Errorf("rea needs %s, which isn't installed", tool)
		}
	}
	unlock, err := reaLock(0)
	if err != nil {
		return "", err
	}
	defer unlock()
	st := loadReaState()
	defer func() {
		line := result
		if err != nil {
			line = "error: " + err.Error()
		}
		st.Checked = &reaCheck{At: time.Now().UTC(), Result: line}
		if serr := st.save(); serr != nil && err == nil {
			err = serr
		}
	}()
	st.cleanup()

	tag := reaTagPrefix + pin
	if pin == "" {
		if tag, err = src.latestTag(ctx, &st); err != nil {
			return "", err
		}
	}
	v := strings.TrimPrefix(tag, reaTagPrefix)
	if _, ok := reaSemver(v); !ok || !strings.HasPrefix(tag, reaTagPrefix) {
		return "", fmt.Errorf("%q is not a rea release tag", tag)
	}
	if reaInstalled(st.Current) && (v == st.Current || pin == "" && !reaNewer(v, st.Current)) {
		if err := migrateRea(); err != nil {
			return "", err
		}
		return "rea " + st.Current + " is the newest release", nil
	}
	if why, bad := st.Bad[v]; bad {
		return fmt.Sprintf("rea %s is skipped (%s); waiting for a newer release", v, why), nil
	}
	if pin != "" && reaInstalled(pin) {
		// Putting back a version still on disk: it was verified when it
		// went in.
		st.swap(pin)
		return "rea " + pin + " is back in use", migrateRea()
	}

	commit, err := src.tagCommit(ctx, tag)
	if err != nil {
		return "", err
	}
	tarball, integrity, attestations, err := src.packageMeta(ctx, v)
	if err != nil {
		return "", err
	}
	want, err := npmSHA512(integrity)
	if err != nil {
		return "", err
	}
	pkg, err := src.get(ctx, tarball, 512<<20)
	if err != nil {
		return "", err
	}
	got := sha512.Sum512(pkg)
	sum := hex.EncodeToString(got[:])
	if !bytes.Equal(got[:], want) {
		return "", fmt.Errorf("rea %s: the download doesn't match the registry's SHA-512", v)
	}
	if p, ok := st.Pins[v]; ok && p.SHA512 != sum {
		st.markBad(v, "its package changed after it was verified on "+p.Verified.Format("2006-01-02"))
		return "", fmt.Errorf("rea %s: the package is not the one verified on %s; refused", v, p.Verified.Format("2006-01-02"))
	}
	att, err := src.get(ctx, attestations, 8<<20)
	if err != nil {
		return "", err
	}
	bundle, err := reaProvenanceBundle(att)
	if err == nil {
		err = src.Verify(bundle, v, got[:], commit)
	}
	if err != nil {
		var u unverified
		if errors.As(err, &u) || bundle == nil {
			st.markBad(v, "its provenance didn't verify")
		}
		return "", fmt.Errorf("rea %s not installed: %w", v, err)
	}

	if err := os.MkdirAll(reaVersionsDir(), 0o700); err != nil {
		return "", err
	}
	partial, err := os.MkdirTemp(reaVersionsDir(), v+".partial-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(partial)
	file := filepath.Join(partial, reaPackage+"-"+v+".tgz")
	if err := os.WriteFile(file, pkg, 0o600); err != nil {
		return "", err
	}
	if err := src.Install(ctx, partial, file, v); err != nil {
		return "", fmt.Errorf("rea %s didn't install: %w", v, err)
	}
	final := reaVersionDir(v)
	if err := os.RemoveAll(final); err != nil {
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		return "", err
	}
	if err := src.Check(ctx, v); err != nil {
		os.RemoveAll(final)
		if ctx.Err() == nil {
			st.markBad(v, "it didn't start: "+err.Error())
		}
		return "", fmt.Errorf("rea %s installed but didn't start, so it isn't used: %w", v, err)
	}
	if st.Pins == nil {
		st.Pins = map[string]reaPin{}
	}
	st.Pins[v] = reaPin{Tag: tag, Commit: commit, SHA512: sum, Verified: time.Now().UTC()}
	from := st.Current
	st.swap(v)
	if err := migrateRea(); err != nil {
		return "", err
	}
	if reaInstalled(from) {
		return fmt.Sprintf("updated rea from %s to %s", from, v), nil
	}
	return "installed rea " + v, nil
}

// swap makes v the version sessions start, keeping the one it replaces.
func (st *reaState) swap(v string) {
	old := st.Current
	if old != v && reaInstalled(old) {
		if st.Previous != "" && st.Previous != v {
			st.retire(st.Previous)
		}
		st.Previous = old
	}
	if st.Previous == v {
		st.Previous = ""
	}
	st.Current = v
	delete(st.Retired, v)
}

func (st *reaState) retire(v string) {
	if st.Retired == nil {
		st.Retired = map[string]time.Time{}
	}
	if _, ok := st.Retired[v]; !ok {
		st.Retired[v] = time.Now().UTC()
	}
}

func (st *reaState) markBad(v, why string) {
	if st.Bad == nil {
		st.Bad = map[string]string{}
	}
	st.Bad[v] = why
}

// cleanup takes away what an interrupted install left, and versions
// retired more than three days ago.
func (st *reaState) cleanup() {
	entries, _ := os.ReadDir(reaVersionsDir())
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.Contains(name, ".partial-"):
			os.RemoveAll(filepath.Join(reaVersionsDir(), name))
		case name == st.Current || name == st.Previous:
		default:
			st.retire(name)
			if time.Since(st.Retired[name]) > 72*time.Hour && os.RemoveAll(reaVersionDir(name)) == nil {
				delete(st.Retired, name)
			}
		}
	}
}

// migrateRea turns the npx entry SeedDefaults put in mcp.json into the
// managed one, once a verified version is installed; on or off stays as
// it was. Any other entry is left alone.
func migrateRea() error {
	f, err := ReadFile(UserPath())
	if err != nil {
		return err
	}
	e, ok := f.Servers["rea"]
	if !ok || !seededRea(e) {
		return nil
	}
	s := ReaServer
	s.Disabled = e.Disabled
	f.Servers["rea"] = s
	return WriteFile(UserPath(), f)
}

func (src reaSource) request(ctx context.Context, u string, header map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "caveira")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return src.HTTP.Do(req)
}

// get is the body at u, up to limit bytes.
func (src reaSource) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	resp, err := src.request(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d MB", u, limit>>20)
	}
	return b, nil
}

func (src reaSource) getJSON(ctx context.Context, u string, v any) error {
	b, err := src.get(ctx, u, 4<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// latestTag is the tag of rea's newest release: published, not a draft
// or a pre-release. GitHub is asked with the last answer's ETag, so an
// unchanged answer doesn't count against its rate limit.
func (src reaSource) latestTag(ctx context.Context, st *reaState) (string, error) {
	h := map[string]string{"Accept": "application/vnd.github+json"}
	if st.ETag != "" && st.Latest != "" {
		h["If-None-Match"] = st.ETag
	}
	resp, err := src.request(ctx, src.GitHub+"/repos/"+reaRepo+"/releases/latest", h)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return st.Latest, nil
	case http.StatusOK:
	default:
		return "", fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return "", err
	}
	if rel.Draft || rel.Prerelease || rel.Tag == "" {
		return "", errors.New("GitHub's latest rea release is not a published release")
	}
	st.ETag, st.Latest = resp.Header.Get("ETag"), rel.Tag
	return rel.Tag, nil
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// tagCommit is the commit tag points at, through an annotated tag if it
// is one.
func (src reaSource) tagCommit(ctx context.Context, tag string) (string, error) {
	type object struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	var o object
	if err := src.getJSON(ctx, src.GitHub+"/repos/"+reaRepo+"/git/ref/tags/"+url.PathEscape(tag), &o); err != nil {
		return "", err
	}
	if o.Object.Type == "tag" {
		var t object
		if err := src.getJSON(ctx, src.GitHub+"/repos/"+reaRepo+"/git/tags/"+o.Object.SHA, &t); err != nil {
			return "", err
		}
		o = t
	}
	if o.Object.Type != "commit" || !commitSHA.MatchString(o.Object.SHA) {
		return "", fmt.Errorf("tag %s doesn't point at a commit", tag)
	}
	return o.Object.SHA, nil
}

// packageMeta is where the registry keeps rea-agents at v, its integrity,
// and where its attestations are. Both addresses have to be on the
// registry itself.
func (src reaSource) packageMeta(ctx context.Context, v string) (tarball, integrity, attestations string, err error) {
	var m struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball      string `json:"tarball"`
			Integrity    string `json:"integrity"`
			Attestations struct {
				URL string `json:"url"`
			} `json:"attestations"`
		} `json:"dist"`
	}
	if err := src.getJSON(ctx, src.Registry+"/"+reaPackage+"/"+v, &m); err != nil {
		return "", "", "", err
	}
	if m.Version != v {
		return "", "", "", fmt.Errorf("the registry answered with %s for %s", m.Version, v)
	}
	for _, u := range []string{m.Dist.Tarball, m.Dist.Attestations.URL} {
		if !strings.HasPrefix(u, src.Registry+"/") {
			return "", "", "", fmt.Errorf("rea %s: %q is not on the registry", v, u)
		}
	}
	return m.Dist.Tarball, m.Dist.Integrity, m.Dist.Attestations.URL, nil
}

// npmInstall installs the verified package in dir with its dependencies,
// from the public registry. No package's install script runs until
// every package installed has had its registry signature checked; then
// they run, so native parts get built.
func npmInstall(ctx context.Context, dir, tarball, version string) error {
	pkg, _ := json.Marshal(map[string]any{
		"name": "caveira-rea", "version": "0.0.0", "private": true,
		"dependencies": map[string]string{reaPackage: "file:" + filepath.Base(tarball)},
	})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), pkg, 0o600); err != nil {
		return err
	}
	reg := "--registry=" + npmRegistry + "/"
	for _, args := range [][]string{
		{"install", "--ignore-scripts", "--no-audit", "--no-fund", reg},
		{"audit", "signatures", reg},
		{"rebuild"},
	} {
		if err := npm(ctx, dir, args...); err != nil {
			return err
		}
	}
	var got struct {
		Version string `json:"version"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "node_modules", reaPackage, "package.json"))
	if err == nil {
		err = json.Unmarshal(b, &got)
	}
	if err != nil || got.Version != version {
		return fmt.Errorf("npm put %q in place of %s", got.Version, version)
	}
	return nil
}

func npm(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Dir = dir
	proc.Hide(cmd)
	cmd.Env = append(os.Environ(), "npm_config_update_notifier=false", "npm_config_fund=false")
	out := &tail{max: 2048}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm %s: %v: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	return nil
}

// checkRea starts installed version v the way a session would, in an
// empty folder, and has it list its tools.
func checkRea(ctx context.Context, v string) error {
	work, err := os.MkdirTemp("", "caveira-rea-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	m := NewManager(work, "rea-check")
	c := &conn{entry: Entry{Name: "rea", Server: Server{Command: "node", Args: []string{reaScript(v), "mcp"}}}}
	session, list, err := m.dial(ctx, c)
	if err != nil {
		return errors.New(describe(err, c.stderr))
	}
	session.Close()
	if len(list) == 0 {
		return errors.New("it started but offered no tools")
	}
	return nil
}
