package agent

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/config"
	"github.com/justin06lee/caveira/core/llm"
)

// Session is a conversation on disk, so a run can be picked up later.
type Session struct {
	ID      string `json:"id"`
	WorkDir string `json:"work_dir"`
	Model   string `json:"model"`
	Title   string `json:"title"`
	// Source names the app a chat was imported from ("Claude Code"),
	// empty for one caveira started.
	Source    string        `json:"source,omitempty"`
	Origin    *Origin       `json:"origin,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []llm.Message `json:"messages"`
	Totals    Totals        `json:"totals"`
	// Loaded is the deferred MCP tools the model loaded with tool_search,
	// sent again when the session is continued.
	Loaded []string `json:"loaded_tools,omitempty"`
	// Exchanges is how many exchanges have been recorded in Dir.
	Exchanges int `json:"exchanges,omitempty"`
}

// Origin is where an imported chat is read from, and how much of it the
// copy here holds, so it can be brought up to date when the other app
// carries it on.
type Origin struct {
	Path string    `json:"path"` // the other app's file for it, or its database
	At   time.Time `json:"at"`   // when that last changed, as of this copy
	// Len is how many of the messages here came from it. A copy with any
	// other number was carried on in caveira, or Len is -1 for one found
	// to have been, and is left as it is.
	Len int `json:"len"`
}

func sessionsDir() string { return filepath.Join(config.Dir(), "sessions") }

// Path is the session's file.
func (s *Session) Path() string { return filepath.Join(sessionsDir(), s.ID+".json") }

// NewSession starts an empty session for the agent. Nothing is written until
// the first turn completes.
func (a *Agent) NewSession() *Session {
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	now := time.Now()
	a.Session = &Session{
		ID:        now.Format("20060102-150405") + "-" + hex.EncodeToString(rnd[:]),
		WorkDir:   a.WorkDir,
		Model:     a.Model,
		CreatedAt: now,
	}
	return a.Session
}

// Attach continues a stored session.
func (a *Agent) Attach(s *Session) {
	a.Session = s
	a.Messages = s.Messages
	a.Totals = s.Totals
	if a.Tools != nil {
		a.Tools.Unload()
		a.Tools.Load(s.Loaded...)
	}
	if s.Model != "" && s.Model != a.Model {
		// The model the session was recorded with is kept in the file for
		// reference; the current configuration decides what runs now.
		s.Model = a.Model
	}
}

func (a *Agent) save() {
	if a.Session == nil {
		return
	}
	s := a.Session
	s.Messages = a.Messages
	s.Totals = a.Totals
	if a.Tools != nil {
		s.Loaded = a.Tools.Loaded()
	}
	s.Model = a.Model
	s.UpdatedAt = time.Now()
	if s.Title == "" {
		for _, m := range a.Messages {
			if m.Role == llm.RoleUser {
				s.Title = firstLine(m.Content, 80)
				break
			}
		}
	}
	if len(s.Messages) == 0 {
		return
	}
	_ = s.Save()
}

// Save writes the session to its file.
func (s *Session) Save() error {
	if err := os.MkdirAll(sessionsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := s.Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path())
}

// FirstLineTitle is the title a session gets from its first message
// when nothing names it.
func FirstLineTitle(first string) string { return firstLine(first, 80) }

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// LoadSession reads one session by ID.
func LoadSession(id string) (*Session, error) {
	if !validSessionID(id) {
		return nil, errors.New("not a session id: " + id)
	}
	b, err := os.ReadFile(filepath.Join(sessionsDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// HasSession says whether a session with this id is saved.
func HasSession(id string) bool {
	if !validSessionID(id) {
		return false
	}
	_, err := os.Stat(filepath.Join(sessionsDir(), id+".json"))
	return err == nil
}

// validSessionID says id can only name a file in the sessions folder: no
// separator of either kind (a backslash separates folders on Windows), no
// colon (a drive, or an NTFS data stream there) and not a dot name.
func validSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\:\x00")
}

// DeleteSession removes one session's file.
func DeleteSession(id string) error {
	if !validSessionID(id) {
		return errors.New("not a session id: " + id)
	}
	_ = os.RemoveAll(filepath.Join(sessionsDir(), id))
	return os.Remove(filepath.Join(sessionsDir(), id+".json"))
}

// roleCompaction marks where a compaction was, in the archive.
const roleCompaction = "compaction"

// archive adds the messages a compaction retires to the session's
// archive, then a mark for the compaction. Earlier briefs become marks;
// the replies acknowledging them go.
func archive(dir string, msgs []llm.Message) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "archive.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if isBrief(m) {
			_ = enc.Encode(llm.Message{Role: roleCompaction})
			if i+1 < len(msgs) && isAck(msgs[i+1]) {
				i++
			}
			continue
		}
		_ = enc.Encode(m)
	}
	_ = enc.Encode(llm.Message{Role: roleCompaction})
}

// writeBriefFile keeps each brief as it went out, numbered.
func writeBriefFile(dir, brief string) {
	n := 1
	if entries, err := os.ReadDir(filepath.Join(dir, "briefs")); err == nil {
		n = len(entries) + 1
	}
	_ = writeAtomic(filepath.Join(dir, "briefs", fmt.Sprintf("%04d.md", n)), []byte(brief))
}

// History is the whole conversation as it happened, for drawing a
// transcript: what compactions retired, then the live messages. Each
// compaction is a message from the user starting "This session's earlier
// conversation was compacted", as the clients already draw one.
func (s *Session) History() []llm.Message {
	var out []llm.Message
	if f, err := os.Open(filepath.Join(s.Dir(), "archive.jsonl")); err == nil {
		dec := json.NewDecoder(f)
		for {
			var m llm.Message
			if dec.Decode(&m) != nil {
				break
			}
			if m.Role == roleCompaction {
				m = llm.Message{Role: llm.RoleUser, Content: compactedPrefix + "."}
			}
			out = append(out, m)
		}
		f.Close()
	}
	if len(out) == 0 {
		return s.Messages
	}
	for i := 0; i < len(s.Messages); i++ {
		// The live brief is the archive's last mark.
		if isBrief(s.Messages[i]) {
			if i+1 < len(s.Messages) && isAck(s.Messages[i+1]) {
				i++
			}
			continue
		}
		out = append(out, s.Messages[i])
	}
	return out
}

// LatestSession finds the most recently updated session for workDir.
func LatestSession(workDir string) (*Session, error) {
	sessions, err := ListSessions(workDir)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, errors.New("no previous session for this directory")
	}
	return LoadSession(sessions[0].ID)
}

// ListSessions returns session headers for workDir (or all when empty),
// newest first. Messages are not loaded, or read: an imported chat can be
// megabytes, and the sidebar asks for this list after every turn.
func ListSessions(workDir string) ([]Session, error) {
	entries, err := os.ReadDir(sessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := readHeader(filepath.Join(sessionsDir(), e.Name()))
		if err != nil || s.ID == "" {
			continue
		}
		if workDir != "" && s.WorkDir != workDir {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// readHeader reads a session file as far as its messages. Save writes the
// header fields first, so that is all of them but the totals.
func readHeader(path string) (Session, error) {
	var s Session
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return s, errors.New("not a session")
	}
	fields := map[string]any{
		"id":         &s.ID,
		"work_dir":   &s.WorkDir,
		"model":      &s.Model,
		"title":      &s.Title,
		"source":     &s.Source,
		"created_at": &s.CreatedAt,
		"updated_at": &s.UpdatedAt,
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return s, err
		}
		key, _ := t.(string)
		if key == "messages" && s.ID != "" {
			return s, nil
		}
		dst, ok := fields[key]
		if !ok {
			dst = new(json.RawMessage)
		}
		if err := dec.Decode(dst); err != nil {
			return s, err
		}
	}
	return s, nil
}
