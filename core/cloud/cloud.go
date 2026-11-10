// Package cloud is the caveira account: signing in from the terminal or
// the desktop app through the caveira website, and the token that comes
// back, kept in ~/.caveira/auth.json.
//
// Signing in is a device login. The client asks the site for a code,
// opens the site's /device page with it in the browser, where the person
// signs in (or already is) and connects this machine, and polls until the
// site hands over a token. Model requests then go to the site's
// OpenAI-compatible /api/v1 with that token as the bearer; the site
// checks the account's plan and usage, and answers them.
//
// The site's side of it:
//
//	POST /api/auth/device/start  {client, label}  → Code
//	POST /api/auth/device/poll   {device_code}    → {token, email} or {error}
//	GET  /api/me                 bearer           → Account
//	POST /api/auth/logout        bearer           → the token stops working
//	GET  /api/v1/models, POST /api/v1/chat/completions, bearer
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/justin06lee/caveira/core/devserver"
	"github.com/justin06lee/caveira/core/private"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultURL is the caveira website.
const DefaultURL = "https://caveira.app"

// URL is the site to sign in through: DefaultURL, or in a development
// build (package devserver) CAVEIRA_CLOUD_URL when it is set.
func URL() string {
	if v := strings.TrimSpace(os.Getenv("CAVEIRA_CLOUD_URL")); v != "" && devserver.Enabled {
		return strings.TrimRight(v, "/")
	}
	return DefaultURL
}

// Auth is a signed-in account on this machine.
type Auth struct {
	// URL is the site the token belongs to.
	URL   string `json:"url"`
	Token string `json:"token"`
	Email string `json:"email"`
}

// APIBase is where the account's model requests go.
func (a Auth) APIBase() string { return strings.TrimRight(a.URL, "/") + "/api/v1" }

// Path is the file the account is kept in.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".caveira", "auth.json")
	}
	return filepath.Join(home, ".caveira", "auth.json")
}

// Load is the account this machine is signed in to, if any.
func Load() (Auth, bool) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return Auth{}, false
	}
	var a Auth
	if json.Unmarshal(b, &a) != nil || a.Token == "" || a.URL == "" {
		return Auth{}, false
	}
	// A shipped build talks to caveira.app whatever the file says: an
	// address edited in can't send requests anywhere else.
	if !devserver.Enabled {
		a.URL = DefaultURL
	}
	return a, true
}

// Save keeps a, for its owner alone (see package private) because the
// token is as good as a password.
func Save(a Auth) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return private.WriteFile(Path(), append(b, '\n'))
}

// Code is a login waiting for the person to connect it in the browser.
type Code struct {
	DeviceCode string `json:"device_code"`
	// UserCode is shown on both screens, so the person can see the page
	// they are approving is the login they started.
	UserCode string `json:"user_code"`
	// VerifyURL is the /device page with the code filled in.
	VerifyURL string `json:"verification_uri_complete"`
	// Interval is how many seconds to wait between polls.
	Interval  int `json:"interval"`
	ExpiresIn int `json:"expires_in"`
}

// Start asks site for a login code. client is "tui" or "desktop"; label
// names this machine on the account page.
func Start(ctx context.Context, site, client, label string) (Code, error) {
	var c Code
	err := call(ctx, http.MethodPost, site+"/api/auth/device/start", "",
		map[string]string{"client": client, "label": label}, &c)
	if err != nil {
		return Code{}, err
	}
	if c.DeviceCode == "" || c.VerifyURL == "" {
		return Code{}, errors.New("the caveira site sent back no login code")
	}
	if c.Interval <= 0 {
		c.Interval = 2
	}
	return c, nil
}

// Errors Wait ends with when the login does not go through.
var (
	ErrDenied  = errors.New("the login was cancelled in the browser")
	ErrExpired = errors.New("the login code expired before it was used")
)

// Wait polls until the person connects the code in the browser, then
// keeps the account on this machine and returns it.
func Wait(ctx context.Context, site string, c Code) (Auth, error) {
	interval := time.Duration(c.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return Auth{}, ctx.Err()
		case <-time.After(interval):
		}
		var out struct {
			Token string `json:"token"`
			Email string `json:"email"`
		}
		err := call(ctx, http.MethodPost, site+"/api/auth/device/poll", "",
			map[string]string{"device_code": c.DeviceCode}, &out)
		var se *SiteError
		switch {
		case err == nil:
			a := Auth{URL: site, Token: out.Token, Email: out.Email}
			return a, Save(a)
		case errors.As(err, &se) && se.Code == "authorization_pending":
		case errors.As(err, &se) && se.Code == "slow_down":
			interval += 2 * time.Second
		case errors.As(err, &se) && se.Code == "access_denied":
			return Auth{}, ErrDenied
		case errors.As(err, &se) && se.Code == "expired_token":
			return Auth{}, ErrExpired
		default:
			return Auth{}, err
		}
	}
}

// Account is what the site says about the signed-in account.
type Account struct {
	Email string `json:"email"`
	// Admin says the account has no allowance to run out of; its plan is
	// called "Admin" and its usage is not counted against anything.
	Admin bool `json:"admin"`
	Plan  struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Large says the plan runs the large models.
		Large bool `json:"large"`
		// Status is the subscription's, "" on Free.
		Status string `json:"status"`
	} `json:"plan"`
	Usage struct {
		// Used is the share of this period's usage already spent, 0 to 1.
		Used float64 `json:"used"`
		// ResetsAt is when it starts again, RFC 3339.
		ResetsAt string `json:"resets_at"`
	} `json:"usage"`
}

// allowances are how each plan's usage reads, as on caveira.app/pricing:
// the multiple of Free's monthly usage it gets, never in dollars.
var allowances = map[string]string{
	"free": "Light usage", "lightweight": "10x the usage of Free", "middleweight": "25x the usage of Free",
	"heavyweight": "50x the usage of Free", "champion": "100x the usage of Free",
}

// Allowance is how the plan with this id states its usage, "" for a plan
// caveira does not know.
func Allowance(plan string) string { return allowances[plan] }

// PlanLine is the account's plan as one line: "Lightweight · 10x the usage of Free",
// or "Admin · unlimited".
func (a Account) PlanLine() string {
	name := a.Plan.Name
	if a.Admin {
		if name == "" {
			name = "Admin"
		}
		return name + " · unlimited"
	}
	if u := Allowance(a.Plan.ID); u != "" {
		return name + " · " + u
	}
	return name
}

// Me asks the site who a is signed in as, and on what plan.
func Me(ctx context.Context, a Auth) (Account, error) {
	var acct Account
	err := call(ctx, http.MethodGet, strings.TrimRight(a.URL, "/")+"/api/me", a.Token, nil, &acct)
	return acct, err
}

// Logout ends the session on the site and forgets it here. The local
// file goes even when the site cannot be reached.
func Logout(ctx context.Context) error {
	a, ok := Load()
	if !ok {
		return nil
	}
	err := call(ctx, http.MethodPost, strings.TrimRight(a.URL, "/")+"/api/auth/logout", a.Token, nil, nil)
	if rmErr := os.Remove(Path()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return rmErr
	}
	var se *SiteError
	if errors.As(err, &se) && se.Status == http.StatusUnauthorized {
		return nil // already signed out there
	}
	return err
}

// SiteError is an error answer from the caveira site.
type SiteError struct {
	Status  int
	Code    string
	Message string
}

func (e *SiteError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("the caveira site answered HTTP %d", e.Status)
}

// Unauthorized says the token is no longer good: sign in again.
func Unauthorized(err error) bool {
	var se *SiteError
	return errors.As(err, &se) && se.Status == http.StatusUnauthorized
}

var client = &http.Client{Timeout: 30 * time.Second}

func call(ctx context.Context, method, url, token string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "caveira")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", hostOf(url), err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		se := &SiteError{Status: res.StatusCode}
		var eb struct {
			Error            json.RawMessage `json:"error"`
			ErrorDescription string          `json:"error_description"`
			Message          string          `json:"message"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			// {"error": "authorization_pending"} or {"error": {"message": …}}
			var code string
			var obj struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			}
			if json.Unmarshal(eb.Error, &code) == nil {
				se.Code = code
			} else if json.Unmarshal(eb.Error, &obj) == nil {
				se.Code, se.Message = obj.Code, obj.Message
			}
			if se.Message == "" {
				se.Message = eb.ErrorDescription
			}
			if se.Message == "" {
				se.Message = eb.Message
			}
		}
		return se
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from %s: %w", hostOf(url), err)
	}
	return nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Label names this machine for the account page: its host name.
func Label() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(h, ".local")
}
