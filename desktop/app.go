package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justin06lee/caveira/core/cloud"
	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/importer"
	"github.com/justin06lee/caveira/core/llm"
	"github.com/justin06lee/caveira/core/mcp"
	"github.com/justin06lee/caveira/core/proc"
	"github.com/justin06lee/caveira/core/update"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is what the window talks to. Every exported method is callable from
// the frontend; events go the other way through emit.
type App struct {
	ctx     context.Context
	version string

	mu    sync.Mutex
	prefs prefs
	chats map[string]*chat
	// gen counts settings changes. A chat built under an older gen is
	// rebuilt from the new settings before its next turn.
	gen int
	// sink, when set, takes events instead of the Wails runtime: the
	// browser harness in harness_test.go.
	sink func(name string, data any)
	// found is what the last look for other agents' chats found.
	found []importer.Chat
	// namer names a new chat from its first message; nil leaves chats
	// with that message's first line, as the tests do.
	namer func(ctx context.Context, client *llm.Client, first string) (string, error)

	// acct is the caveira account, and signing a sign-in waiting on the
	// browser (account.go).
	acct    account
	signing *signIn
	// watching says the account is being asked about every watchEvery,
	// until watchUntil, for the chats holding a message.
	watching   bool
	watchUntil time.Time
	// browse opens a page of the caveira site in the browser; the tests
	// and the harness only note it.
	browse func(url string) error
	// logPath is the log this run writes, and reveal shows it in the
	// Finder (logs.go).
	logPath string
	reveal  func(path string) error

	// updates is the release feed and update what it last said
	// (update.go).
	updates *update.Client
	update  UpdateView

	// mcp is the MCP servers of each project a chat has opened in, by
	// project root, and listings the last registry search
	// (extensions.go).
	mcp      map[string]*mcp.Manager
	listings map[string]mcp.Listing

	// relay is Phone companion running, nil while it is off (relay.go).
	relay atomic.Pointer[relay]
}

func NewApp(version string) *App {
	a := &App{version: version, prefs: loadPrefs(), chats: map[string]*chat{}, namer: nameChat, browse: cloud.OpenBrowser, reveal: revealInFinder, updates: &update.Client{}}
	// Signed in or not is on disk; the plan comes when the site is asked.
	a.acct.auth, _ = cloud.Load()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.watchUpdates(ctx)
	go a.updateRea(ctx)
	if a.Companion() {
		a.startRelay()
	}
}

func (a *App) emit(name string, data any) {
	a.relayNote(name, data)
	if a.sink != nil {
		a.sink(name, data)
		return
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, name, data)
	}
}

// TitleBarDoubleClick does what a double-click on a window's title bar
// does: zooms the window, or back. A Mac set to minimize instead, or to do
// nothing, gets that.
func (a *App) TitleBarDoubleClick() {
	if a.ctx == nil {
		return
	}
	switch doubleClickAction() {
	case "Minimize":
		runtime.WindowMinimise(a.ctx)
	case "None":
	default:
		runtime.WindowToggleMaximise(a.ctx)
	}
}

// doubleClickAction is the Mac's "Double-click a window's title bar to"
// setting: Maximize (Zoom), Fill, Minimize, or None; "" when unset, which
// is Zoom, and elsewhere.
func doubleClickAction() string {
	if goruntime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleActionOnDoubleClick").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Project is a folder caveira works in.
type Project struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Short  string `json:"short"` // the path with ~ for the home directory
	Branch string `json:"branch"`
}

// Boot is everything the window needs to draw its first frame.
type Boot struct {
	Version   string       `json:"version"`
	Settings  SettingsView `json:"settings"`
	Projects  []Project    `json:"projects"`
	Workspace Workspace    `json:"workspace"`
	// Onboarded is false until the first-run steps are done.
	Onboarded bool `json:"onboarded"`
	// Account is the caveira account as this machine knows it, before
	// the site is asked (Account does that), and Plans what it can be on.
	Account AccountView `json:"account"`
	Plans   []Plan      `json:"plans"`
	// Update is a newer release, if one is known yet.
	Update UpdateView `json:"update"`
}

func (a *App) Boot() Boot {
	waitShellEnv()
	a.mu.Lock()
	dirs := a.prefs.existingProjects()
	ws, onboarded, acct := a.prefs.Workspace, a.prefs.Onboarded, a.accountView()
	a.mu.Unlock()
	return Boot{
		Version: a.version, Settings: a.Settings(), Projects: projects(dirs), Workspace: workspace(ws),
		Onboarded: onboarded, Account: acct, Plans: plans, Update: a.Update(),
	}
}

// Projects is the recent projects, for after an import adds some.
func (a *App) Projects() []Project {
	a.mu.Lock()
	dirs := a.prefs.existingProjects()
	a.mu.Unlock()
	return projects(dirs)
}

// projects is the sidebar's list. One in a folder macOS guards gets its
// branch once opened, so listing it asks the user for nothing.
func projects(dirs []string) []Project {
	out := make([]Project, 0, len(dirs))
	for _, d := range dirs {
		if guardedPath(d) {
			out = append(out, Project{Path: d, Name: filepath.Base(d), Short: shortPath(d)})
			continue
		}
		out = append(out, project(d))
	}
	return out
}

func project(dir string) Project {
	return Project{Path: dir, Name: filepath.Base(dir), Short: shortPath(dir), Branch: gitBranch(dir)}
}

// OpenProject makes dir the most recent project. The app has no notion
// of a current project; the frontend keeps that.
func (a *App) OpenProject(dir string) (Project, error) {
	dir = filepath.Clean(dir)
	st, err := os.Stat(dir)
	if err != nil {
		return Project{}, err
	}
	if !st.IsDir() {
		return Project{}, fmt.Errorf("%s is not a folder", dir)
	}
	a.mu.Lock()
	a.prefs.touch(dir)
	err = a.prefs.save()
	a.mu.Unlock()
	return project(dir), err
}

// ChooseProject asks for a folder. An empty path means the user cancelled.
func (a *App) ChooseProject() (Project, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "Open a project",
		CanCreateDirectories: true,
	})
	if err != nil || dir == "" {
		return Project{}, err
	}
	return a.OpenProject(dir)
}

func (a *App) ForgetProject(dir string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prefs.forget(dir)
	return a.prefs.save()
}

// Reveal shows a folder in the Finder, Explorer, or the file manager on Linux.
func (a *App) Reveal(dir string) error {
	opener := "open"
	switch goruntime.GOOS {
	case "windows":
		opener = "explorer.exe"
	case "darwin":
	default:
		opener = "xdg-open"
	}
	cmd := exec.Command(opener, dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// SettingsView is the settings screen's state. The app runs the models
// the caveira account offers; there is no key of one's own to set. A local model server set with
// base_url in config.json or CAVEIRA_BASE_URL is used instead, when there
// is one.
type SettingsView struct {
	Model   string   `json:"model"`
	Effort  string   `json:"effort"`
	Confirm bool     `json:"confirm"`
	Theme   string   `json:"theme"`
	Efforts []string `json:"efforts"`
	// Ready is false when a chat could not run: neither the account nor
	// a local server is there to send to.
	Ready bool `json:"ready"`
	// Local says chats go to a local model server, not the account, so
	// the account's usage is not theirs to show.
	Local bool `json:"local"`
}

func (a *App) Settings() SettingsView {
	home, _ := os.UserHomeDir()
	cfg, _ := config.Load(home)
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	return SettingsView{
		Model:   cfg.Model,
		Effort:  cfg.ReasoningEffort,
		Confirm: cfg.Confirm,
		Theme:   p.Theme,
		Efforts: config.Efforts,
		Ready:   cfg.OnAccount() || cfg.Local(),
		Local:   cfg.Local(),
	}
}

// SettingsInput is a change from the settings screen.
type SettingsInput struct {
	Model   string `json:"model"`
	Effort  string `json:"effort"`
	Confirm bool   `json:"confirm"`
	Theme   string `json:"theme"`
}

func (a *App) SaveSettings(in SettingsInput) (SettingsView, error) {
	if in.Effort != "" && !config.ValidEffort(in.Effort) {
		return a.Settings(), fmt.Errorf("reasoning effort must be one of %s", strings.Join(config.Efforts, ", "))
	}
	file, err := config.ReadFile()
	if err != nil {
		return a.Settings(), err
	}
	file.Model = unlessDefault(strings.TrimSpace(in.Model), config.DefaultModel)
	file.ReasoningEffort = in.Effort
	file.Confirm = in.Confirm
	if err := config.Save(file); err != nil {
		return a.Settings(), err
	}

	a.mu.Lock()
	if in.Theme != "" {
		a.prefs.Theme = in.Theme
	}
	err = a.prefs.save()
	a.gen++
	a.mu.Unlock()
	return a.Settings(), err
}

// ModelOption is one model the pickers offer.
type ModelOption struct {
	ID string `json:"id"`
	// Name is what the model is called on screen, under the heading
	// Group, beside the Logos of who makes it.
	Name     string   `json:"name"`
	Group    string   `json:"group,omitempty"`
	Logos    []string `json:"logos,omitempty"`
	Context  int      `json:"context"`
	Note     string   `json:"note"`
	Unusable string   `json:"unusable,omitempty"`
	NoEffort bool     `json:"noEffort,omitempty"`
	// Plan is the cheapest plan that runs it, when Free does not.
	Plan string `json:"plan,omitempty"`
}

// Models lists the models caveira offers through the caveira account. A local model server set instead is asked what it
// serves.
func (a *App) Models() ([]ModelOption, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home, _ := os.UserHomeDir()
	cfg, _ := config.Load(home)
	if isCatalog(cfg) {
		return catalogOptions(), nil
	}
	models, err := llm.New(cfg.BaseURL, cfg.APIKey).Models(ctx)
	out := make([]ModelOption, 0, len(models))
	for _, mi := range models {
		o := ModelOption{ID: mi.ID, Name: mi.ID, Context: mi.ContextLength}
		if config.Known(mi.ID) {
			spec := config.Spec(mi.ID)
			if o.Context == 0 {
				o.Context = spec.ContextWindow
			}
		}
		out = append(out, o)
	}
	return out, err
}

func unlessDefault(v, def string) string {
	if v == def {
		return ""
	}
	return v
}

func shortPath(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return dir
}

// gitBranch is the branch checked out in dir, or "" outside a repository.
func gitBranch(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
