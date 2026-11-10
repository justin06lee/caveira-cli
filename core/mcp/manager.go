package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/justin06lee/caveira/core/proc"
	"github.com/justin06lee/caveira/core/tools"
)

// Status is where a server is.
type Status string

const (
	Starting Status = "starting"
	Ready    Status = "ready"
	Failed   Status = "failed"
	Off      Status = "disabled"
)

// ConnectTimeout is how long a server has to start and list its tools.
// npx and uvx may download the server first, so it is generous.
var ConnectTimeout = 60 * time.Second

// WaitAtTurn is how long a turn waits for servers still starting before
// it goes ahead without them; they join the next turn.
var WaitAtTurn = 20 * time.Second

// State is what the UI shows about one server.
type State struct {
	Name      string     `json:"name"`
	Scope     Scope      `json:"scope"`
	Transport string     `json:"transport"`
	Summary   string     `json:"summary"`
	Status    Status     `json:"status"`
	Error     string     `json:"error,omitempty"`
	Tools     []ToolInfo `json:"tools"`
	// Deferred says the tools are too many to send up front: the model
	// loads them on demand with tool_search.
	Deferred bool `json:"deferred,omitempty"`
}

// ToolInfo is one tool a server offers.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type conn struct {
	entry   Entry
	status  Status
	err     string
	session *sdk.ClientSession
	tools   []*sdk.Tool
	stderr  *tail
	done    chan struct{} // closed when starting is over
	// rea is the version a managed rea entry started (rea.go), and
	// rolledBack what happened when a new one couldn't.
	rea, rolledBack string
}

// Manager runs the servers configured for one working directory.
type Manager struct {
	WorkDir string
	Version string
	// Window, when set, is the model's context size, which decides
	// whether a server's tools are deferred; without it States assumes
	// tools.DefaultWindow.
	Window func() int

	mu    sync.Mutex
	conns map[string]*conn
	// loadErr is what was wrong with the configuration files.
	loadErr error
	closed  bool
}

// NewManager is the servers for workDir, none started yet.
func NewManager(workDir, version string) *Manager {
	return &Manager{WorkDir: workDir, Version: version, conns: map[string]*conn{}}
}

// Start reads the configuration and starts every enabled server in the
// background; Wait waits for them. Called again, it picks up what has
// changed: new servers start, removed or disabled ones stop, and changed
// ones restart.
func (m *Manager) Start() {
	entries, err := Load(m.WorkDir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.loadErr = err
	want := map[string]Entry{}
	for _, e := range entries {
		want[e.Name] = e
	}
	for name, c := range m.conns {
		if e, ok := want[name]; !ok || !reflect.DeepEqual(e, c.entry) {
			c.stop()
			delete(m.conns, name)
		}
	}
	for name, e := range want {
		if _, ok := m.conns[name]; ok {
			continue
		}
		c := &conn{entry: e, status: Starting, done: make(chan struct{})}
		m.conns[name] = c
		if e.Disabled {
			c.status = Off
			close(c.done)
			continue
		}
		go m.connect(c)
	}
}

// Wait waits for servers still starting, up to d or until ctx ends.
func (m *Manager) Wait(ctx context.Context, d time.Duration) {
	m.mu.Lock()
	var pending []chan struct{}
	for _, c := range m.conns {
		pending = append(pending, c.done)
	}
	m.mu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for _, ch := range pending {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		}
	}
}

// Prepare is the agent's Prepare: wait a little for servers still
// starting, then put the ready ones' tools in reg.
func (m *Manager) Prepare(reg *tools.Registry) func(context.Context) {
	return func(ctx context.Context) {
		m.Wait(ctx, WaitAtTurn)
		m.Install(reg)
	}
}

// Install replaces the MCP tools in reg with those of the servers ready
// now.
func (m *Manager) Install(reg *tools.Registry) {
	reg.Unregister(ToolPrefix)
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := map[string]bool{}
	for _, name := range sortedKeys(m.conns) {
		c := m.conns[name]
		if c.status != Ready {
			continue
		}
		for _, t := range c.tools {
			fn := toolName(name, t.Name, taken)
			taken[fn] = true
			reg.Register(&mcpTool{name: fn, server: name, tool: t, session: c.session, workDir: m.WorkDir})
		}
	}
}

// States is every configured server and how it is, by name.
func (m *Manager) States() []State {
	window := 0
	if m.Window != nil {
		window = m.Window()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.conns))
	sizes := map[string]int{}
	for _, name := range sortedKeys(m.conns) {
		c := m.conns[name]
		st := State{
			Name: name, Scope: c.entry.Scope, Transport: c.entry.Transport(),
			Summary: c.summary(), Status: c.status, Error: c.err, Tools: []ToolInfo{},
		}
		for _, t := range c.tools {
			st.Tools = append(st.Tools, ToolInfo{Name: t.Name, Description: firstLine(t.Description)})
			if c.status == Ready {
				sizes[name] += tools.Tokens(&mcpTool{name: toolName(name, t.Name, nil), server: name, tool: t})
			}
		}
		out = append(out, st)
	}
	deferred := tools.Deferred(sizes, window)
	for i := range out {
		out[i].Deferred = deferred[out[i].Name]
	}
	return out
}

// ConfigError is what was wrong with the configuration files, if
// anything.
func (m *Manager) ConfigError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadErr
}

// Close stops every server.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for name, c := range m.conns {
		c.stop()
		delete(m.conns, name)
	}
}

// summary is what the server runs, in one line; for the rea caveira
// manages, its version and where it comes from.
func (c *conn) summary() string {
	if !managedRea(c.entry.Server) {
		return c.entry.Summary()
	}
	v := c.rea
	if v == "" {
		v = ReaVersion()
	}
	switch {
	case v == "":
		return "rea-agents, being installed"
	case c.rolledBack != "":
		return "rea-agents " + v + " · " + c.rolledBack
	}
	return "rea-agents " + v + " · updates itself from its GitHub releases"
}

func (c *conn) stop() {
	if c.session != nil {
		go c.session.Close()
	}
}

func (m *Manager) connect(c *conn) {
	ctx, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
	defer cancel()
	session, list, err := m.dial(ctx, c)
	if err != nil && c.rea != "" {
		// A rea version that has never started in a session, and fails
		// to, is undone and the one before it starts instead.
		failed := c.rea
		if _, ok := reaFailed(failed, describe(err, c.stderr)); ok {
			c.rolledBack = fmt.Sprintf("%s didn't start, so caveira went back", failed)
			retry, cancelRetry := context.WithTimeout(context.Background(), ConnectTimeout)
			session, list, err = m.dial(retry, c)
			cancelRetry()
		}
	}
	if err == nil && c.rea != "" {
		reaStarted(c.rea)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(c.done)
	if m.conns[c.entry.Name] != c || m.closed {
		// Stopped or replaced while it started.
		if session != nil {
			go session.Close()
		}
		return
	}
	if err != nil {
		c.status = Failed
		c.err = describe(err, c.stderr)
		return
	}
	c.session, c.tools, c.status = session, list, Ready
}

func (m *Manager) dial(ctx context.Context, c *conn) (*sdk.ClientSession, []*sdk.Tool, error) {
	e := c.entry
	if err := e.Check(); err != nil {
		return nil, nil, err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "caveira", Version: m.Version}, nil)
	var session *sdk.ClientSession
	var err error
	switch e.Transport() {
	case "stdio":
		var extra map[string]string
		if managedRea(e.Server) {
			v := ReaVersion()
			if v == "" {
				return nil, nil, errors.New("rea isn't installed yet: caveira installs it in the background as it starts")
			}
			c.rea = v
			extra = map[string]string{ReaVar: reaScript(v)}
		}
		cmd := exec.Command(expand(e.Command, extra))
		for _, a := range e.Args {
			cmd.Args = append(cmd.Args, expand(a, extra))
		}
		cmd.Dir = m.WorkDir
		proc.Hide(cmd)
		cmd.Env = os.Environ()
		for k, v := range e.Env {
			cmd.Env = append(cmd.Env, k+"="+expand(v, nil))
		}
		c.stderr = &tail{max: 4096}
		cmd.Stderr = c.stderr
		session, err = client.Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	case "sse":
		session, err = client.Connect(ctx, &sdk.SSEClientTransport{Endpoint: expand(e.URL, nil), HTTPClient: headerClient(e.Headers)}, nil)
	default:
		hc := headerClient(e.Headers)
		session, err = client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: expand(e.URL, nil), HTTPClient: hc, MaxRetries: 1}, nil)
		if err != nil && ctx.Err() == nil {
			// An older server speaks HTTP with server-sent events only.
			if s, serr := client.Connect(ctx, &sdk.SSEClientTransport{Endpoint: expand(e.URL, nil), HTTPClient: hc}, nil); serr == nil {
				session, err = s, nil
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	var list []*sdk.Tool
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			return nil, nil, fmt.Errorf("listing tools: %w", err)
		}
		list = append(list, t)
	}
	return session, list, nil
}

// describe is a start failure in one or two lines, with what the server
// said on stderr when that explains more.
func describe(err error, stderr *tail) string {
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		msg = fmt.Sprintf("did not start within %s", ConnectTimeout)
	}
	if stderr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			lines := strings.Split(s, "\n")
			if len(lines) > 3 {
				lines = lines[len(lines)-3:]
			}
			msg += ": " + strings.Join(lines, " · ")
		}
	}
	return msg
}

type headerRT struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, expand(v, nil))
	}
	return h.next.RoundTrip(r)
}

func headerClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return &http.Client{}
	}
	return &http.Client{Transport: headerRT{headers: headers, next: http.DefaultTransport}}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
