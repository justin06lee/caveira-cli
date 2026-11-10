// Package update keeps caveira current. Releases are published in the
// public justin06lee/caveira-releases repository on GitHub: the terminal
// client as caveira_<os>_<arch>.tar.gz, the app as caveira.dmg, and
// checksums.txt with the SHA-256 of each. A check asks GitHub for the
// latest release at most once per interval (the answer is kept in
// ~/.caveira/update.json); nothing downloaded is used before its checksum
// matches.
//
// Development builds (anything but a plain vX.Y.Z) never ask, and
// CAVEIRA_NO_UPDATE_CHECK=1 or "no_update_check": true in
// ~/.caveira/config.json turns checking off.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/config"
)

// LatestURL is the GitHub API address of the latest release.
const LatestURL = "https://api.github.com/repos/justin06lee/caveira-releases/releases/latest"

// Day is how long the terminal client trusts its last check.
const Day = 24 * time.Hour

const isDarwin = runtime.GOOS == "darwin"

// goos is the system caveira is running on; tests change it to see what
// another system does.
var goos = runtime.GOOS

// ErrUnsupported is what a self-update on Windows comes back with. A
// running .exe cannot be written over, only renamed away first, and the
// Windows builds aren't signed, so a downloaded one would have no signature
// to check. So caveira does not update itself on Windows: it does not ask
// the feed, does not download, and /update says this. Running the installer
// again checks the download against the release's checksums and does it.
var ErrUnsupported = errors.New("caveira does not update itself on Windows yet; to update, run: irm https://caveira.app/install.ps1 | iex")

// Unsupported is ErrUnsupported on a system where caveira cannot update
// itself, and nil elsewhere.
func Unsupported() error {
	if goos == "windows" {
		return ErrUnsupported
	}
	return nil
}

// Release is a published version and the files that came with it.
type Release struct {
	Version string `json:"tag_name"`
	URL     string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Asset is the download address of the file named name, or "".
func (r Release) Asset(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

// Client talks to the releases. The zero value uses GitHub.
type Client struct {
	// Latest is the API address of the latest release; empty is LatestURL.
	Latest string
	HTTP   *http.Client
	// Cache is where the last answer is kept; empty is ~/.caveira/update.json.
	Cache string
	Now   func() time.Time
	// Requirement is the code signature a new app must meet; empty is
	// AppRequirement.
	Requirement string
	// CheckBinary looks over a new terminal client before it goes in; nil
	// is checkBinary. Tests with stand-in binaries replace it.
	CheckBinary func(ctx context.Context, path, version string) error
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) cachePath() string {
	if c.Cache != "" {
		return c.Cache
	}
	return filepath.Join(config.Dir(), "update.json")
}

// Disabled says update checks are turned off, by CAVEIRA_NO_UPDATE_CHECK
// or no_update_check in the settings file.
func Disabled() bool {
	return Unsupported() != nil || checksOff()
}

// checksOff says the user turned update checks off.
func checksOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CAVEIRA_NO_UPDATE_CHECK"))) {
	case "1", "true", "yes", "on":
		return true
	}
	s, _ := config.ReadFile()
	return s.NoUpdateCheck
}

var releaseRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// IsRelease says v is a released version, vX.Y.Z. A build from a
// checkout says more ("v0.2.0-4-gabc1234-dirty", a bare hash, "dev")
// and is a development build.
func IsRelease(v string) bool { return releaseRE.MatchString(strings.TrimSpace(v)) }

func parse(v string) ([3]int, bool) {
	m := releaseRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// Newer says release version a comes after b. Either one not being a
// release makes it false.
func Newer(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// AssetName is the terminal client's archive for a platform.
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("caveira_%s_%s.tar.gz", goos, goarch)
}

// DMG is the app's disk image.
const DMG = "caveira.dmg"

type cached struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

// Check says whether a release newer than current is out, asking GitHub
// only when the last answer is older than maxAge. It answers "" for a
// development build and when checks are off.
func (c *Client) Check(ctx context.Context, current string, maxAge time.Duration) (string, error) {
	if !IsRelease(current) || Disabled() { // Disabled is always true on Windows
		return "", nil
	}
	var last cached
	if b, err := os.ReadFile(c.cachePath()); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	latest := last.Latest
	if last.Checked.IsZero() || c.now().Sub(last.Checked) >= maxAge || c.now().Before(last.Checked) {
		rel, err := c.Fetch(ctx)
		if err != nil {
			return "", err
		}
		latest = rel.Version
		if b, err := json.Marshal(cached{Checked: c.now(), Latest: latest}); err == nil {
			_ = os.MkdirAll(filepath.Dir(c.cachePath()), 0o700)
			_ = os.WriteFile(c.cachePath(), b, 0o600)
		}
	}
	if Newer(latest, current) {
		return latest, nil
	}
	return "", nil
}

// Fetch asks for the latest release.
func (c *Client) Fetch(ctx context.Context) (Release, error) {
	url := c.Latest
	if url == "" {
		url = LatestURL
	}
	if v := os.Getenv("CAVEIRA_RELEASES_URL"); v != "" {
		url = v // a stand-in feed, for testing an update from end to end
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "caveira")
	resp, err := c.http().Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("the release list answered %s", resp.Status)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("reading the latest release: %w", err)
	}
	if !IsRelease(rel.Version) {
		return Release{}, fmt.Errorf("the latest release is named %q, which is not a version", rel.Version)
	}
	return rel, nil
}

// ErrChecksum is a download that does not match the release's checksum.
var ErrChecksum = errors.New("the download does not match the release's checksum")

func (c *Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "caveira")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return resp.Body, nil
}

// checksums reads the release's checksums.txt: name → hex SHA-256.
func (c *Client) checksums(ctx context.Context, rel Release) (map[string]string, error) {
	url := rel.Asset("checksums.txt")
	if url == "" {
		return nil, errors.New("the release has no checksums.txt")
	}
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			sums[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return sums, sc.Err()
}

// Download saves the release's file name into dir and checks it against
// checksums.txt; the path comes back only when it matches, and a file
// that does not is removed.
func (c *Client) Download(ctx context.Context, rel Release, name, dir string) (string, error) {
	if err := Unsupported(); err != nil {
		return "", err
	}
	url := rel.Asset(name)
	if url == "" {
		return "", fmt.Errorf("release %s has no %s", rel.Version, name)
	}
	sums, err := c.checksums(ctx, rel)
	if err != nil {
		return "", err
	}
	want := sums[name]
	if want == "" {
		return "", fmt.Errorf("checksums.txt in %s does not list %s", rel.Version, name)
	}
	body, err := c.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	f, err := os.CreateTemp(dir, ".download-*-"+name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = ErrChecksum
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// NotWritableError is a binary caveira cannot replace itself, because
// root installed it, say. Command is what to run instead.
type NotWritableError struct {
	Dir     string
	Command string
}

func (e *NotWritableError) Error() string {
	return "caveira cannot write to " + e.Dir + "; update it with:\n  " + e.Command
}

// InstallCommand is how to install caveira into dir by hand, with sudo.
func InstallCommand(dir string) string {
	return "curl -fsSL https://caveira.app/install.sh | sudo env CAVEIRA_INSTALL_DIR=" + dir + " sh"
}

// ReplaceBinary puts release rel's terminal client for this platform in
// place of the file target, a link resolved to what it points at, so
// `cav` beside it keeps working. The new file is written next to the old,
// checked (checkBinary) and renamed over it: a session still running the
// old binary keeps its file, and nothing is half-written if the download
// fails. The old file stays at backupPath.
func (c *Client) ReplaceBinary(ctx context.Context, rel Release, target string) error {
	if err := Unsupported(); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".caveira-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return &NotWritableError{Dir: dir, Command: InstallCommand(dir)}
		}
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	done := false
	defer func() {
		if !done {
			os.Remove(tmpName)
		}
	}()

	work, err := os.MkdirTemp("", "caveira-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	archive, err := c.Download(ctx, rel, AssetName(runtime.GOOS, runtime.GOARCH), work)
	if err != nil {
		return err
	}
	if err := extract(archive, "caveira", tmpName); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := c.checkBinary(ctx, tmpName, rel.Version); err != nil {
		return err
	}
	// The old one stays until the new one has started; keepBackup links it
	// first, so the rename below is the only moment anything changes.
	if err := keepBackup(target); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	done = true
	return nil
}

// extract copies the file called name out of a .tar.gz into dst.
func extract(archive, name, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s is not in %s", name, filepath.Base(archive))
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != name {
			continue
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 512<<20))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
}

// CLIPath is where install.sh puts the terminal client.
func CLIPath() string {
	if d := os.Getenv("CAVEIRA_INSTALL_DIR"); d != "" {
		if goos == "windows" {
			return filepath.Join(d, "caveira.exe")
		}
		return filepath.Join(d, "caveira")
	}
	home, _ := os.UserHomeDir()
	if goos == "windows" {
		return filepath.Join(home, ".local", "bin", "caveira.exe")
	}
	return filepath.Join(home, ".local", "bin", "caveira")
}
