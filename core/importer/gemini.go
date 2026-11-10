package importer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/llm"
)

// Gemini CLI keeps each project's chats in ~/.gemini/tmp/<project>/chats,
// where <project> is a short name (listed in ~/.gemini/projects.json, and
// in a .project_root file beside the chats) or, in older versions, the
// sha256 of the project's path. A chat is session-<time>-<id>.jsonl: a
// first line describing the session, then its messages, each written
// again whole when it changes, and {"$set": {…}} lines that replace
// fields of the session, the message list among them. Older versions
// wrote the same record as one session-….json. logs.json beside them
// holds only what the user typed, so it is not read.

func geminiHome(home string) string { return filepath.Join(home, ".gemini") }

type geminiRecord struct {
	SessionID string          `json:"sessionId"`
	Kind      string          `json:"kind"`
	Messages  []geminiMessage `json:"messages"`
}

type geminiMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"` // user, gemini, info, error, warning
	Content   json.RawMessage `json:"content"`
	Thoughts  []struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
	} `json:"thoughts"`
	ToolCalls []struct {
		ID            string          `json:"id"`
		Name          string          `json:"name"`
		Args          map[string]any  `json:"args"`
		Result        json.RawMessage `json:"result"`
		ResultDisplay json.RawMessage `json:"resultDisplay"`
		Status        string          `json:"status"`
	} `json:"toolCalls"`
}

func scanGemini(ctx context.Context, home string) []Chat {
	root := geminiHome(home)
	dirs := geminiProjects(root)
	files, _ := filepath.Glob(filepath.Join(root, "tmp", "*", "chats", "session-*.json*"))
	var out []Chat
	for _, p := range files {
		if ctx.Err() != nil {
			break
		}
		if !strings.HasSuffix(p, ".json") && !strings.HasSuffix(p, ".jsonl") {
			continue
		}
		r, ok := readGemini(p, true)
		// Subagents' chats are written beside the main one's; they are
		// part of it, as Codex's are.
		if !ok || r.SessionID == "" || r.Kind != "" && r.Kind != "main" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		project := filepath.Dir(filepath.Dir(p))
		dir := dirs[filepath.Base(project)]
		if b, err := os.ReadFile(filepath.Join(project, ".project_root")); err == nil {
			dir = strings.TrimSpace(string(b))
		}
		out = append(out, Chat{App: Gemini, ID: r.SessionID, Dir: dir, Updated: info.ModTime(), where: p})
	}
	return out
}

// geminiProjects maps the names of the folders under tmp to the projects
// they are for, both the short names and the older hashes.
func geminiProjects(root string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(root, "projects.json"))
	if err != nil {
		return out
	}
	var f struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(b, &f) != nil {
		return out
	}
	for dir, name := range f.Projects {
		sum := sha256.Sum256([]byte(dir))
		out[hex.EncodeToString(sum[:])] = dir
		out[name] = dir
	}
	return out
}

// readGemini reads a chat file into its record, the messages in the
// order they were first written and as they were last. head stops after
// the line describing the session, which is all Scan needs.
func readGemini(path string, head bool) (geminiRecord, bool) {
	var r geminiRecord
	if strings.HasSuffix(path, ".json") {
		b, err := os.ReadFile(path)
		return r, err == nil && json.Unmarshal(b, &r) == nil
	}
	f, err := os.Open(path)
	if err != nil {
		return r, false
	}
	defer f.Close()
	at := map[string]int{}
	put := func(m geminiMessage) {
		if i, ok := at[m.ID]; ok && m.ID != "" {
			r.Messages[i] = m
			return
		}
		at[m.ID] = len(r.Messages)
		r.Messages = append(r.Messages, m)
	}
	rd := bufio.NewReaderSize(f, 256<<10)
	for first := true; ; first = false {
		raw, rerr := rd.ReadBytes('\n')
		raw = bytes.TrimSpace(raw)
		var l struct {
			geminiMessage
			SessionID string `json:"sessionId"`
			Kind      string `json:"kind"`
			Set       *struct {
				Messages *[]geminiMessage `json:"messages"`
			} `json:"$set"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &l) == nil {
			switch {
			case first:
				if l.SessionID == "" {
					return r, false
				}
				r.SessionID, r.Kind = l.SessionID, l.Kind
				if head {
					return r, true
				}
			case l.Set != nil:
				if l.Set.Messages != nil {
					r.Messages, at = nil, map[string]int{}
					for _, m := range *l.Set.Messages {
						put(m)
					}
				}
			case l.Type != "":
				put(l.geminiMessage)
			}
		}
		if rerr != nil {
			return r, r.SessionID != ""
		}
	}
}

func convertGemini(c Chat) (*agent.Session, error) {
	r, ok := readGemini(c.where, false)
	if !ok {
		return nil, fmt.Errorf("could not read %s", c.where)
	}
	s := &agent.Session{WorkDir: c.Dir}
	var msgs []llm.Message
	for _, m := range r.Messages {
		if t, err := time.Parse(time.RFC3339Nano, m.Timestamp); err == nil {
			if s.CreatedAt.IsZero() {
				s.CreatedAt = t
			}
			s.UpdatedAt = t
		}
		switch m.Type {
		case "user":
			// The first message is the <session_context> block Gemini
			// opens every chat with, which userWords leaves empty.
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userWords(geminiText(m.Content))})
		case "gemini":
			reply := llm.Message{Role: llm.RoleAssistant, Content: geminiText(m.Content)}
			var thoughts []string
			for _, t := range m.Thoughts {
				thoughts = append(thoughts, strings.TrimSpace(join(t.Subject, t.Description)))
			}
			reply.Reasoning = strings.Join(thoughts, "\n\n")
			var results []llm.Message
			for i, tc := range m.ToolCalls {
				if tc.ID == "" {
					tc.ID = fmt.Sprintf("%s_%d", m.ID, i)
				}
				cl := geminiCall(tc.ID, tc.Name, tc.Args)
				reply.ToolCalls = append(reply.ToolCalls, cl)
				out := geminiResult(tc.Result)
				if tc.Status == "success" && cl.Function.Name == "edit_file" {
					var d struct {
						FileDiff string `json:"fileDiff"`
					}
					if json.Unmarshal(tc.ResultDisplay, &d) == nil && d.FileDiff != "" {
						var a struct {
							Path string `json:"path"`
						}
						_ = json.Unmarshal([]byte(cl.Function.Arguments), &a)
						out = edited(a.Path, hunksOnly(d.FileDiff))
					}
				}
				if out == "" && tc.Status != "success" {
					out = "Error: the call ended " + firstNonEmpty(tc.Status, "without a result") + "."
				}
				results = append(results, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: out})
			}
			msgs = append(msgs, reply)
			msgs = append(msgs, results...)
		}
	}
	s.Messages = msgs
	return s, nil
}

// geminiText reads message content: a string, one part or a list of
// parts, of which the text ones are kept.
func geminiText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		raw = append(append([]byte("["), raw...), ']')
	}
	return text(raw)
}

// geminiResult is what a tool sent back to the model: the output or error
// of its functionResponse parts, or the text parts beside them.
func geminiResult(raw json.RawMessage) string {
	var parts []struct {
		Text     string `json:"text"`
		Response *struct {
			Response map[string]any `json:"response"`
		} `json:"functionResponse"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		raw = append(append([]byte("["), raw...), ']')
	}
	if json.Unmarshal(raw, &parts) != nil {
		return text(raw)
	}
	var out []string
	for _, p := range parts {
		switch {
		case p.Response != nil:
			for _, k := range []string{"output", "error"} {
				if v, ok := p.Response.Response[k].(string); ok && v != "" {
					if k == "error" {
						v = "Error: " + v
					}
					out = append(out, v)
				}
			}
		case p.Text != "":
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// geminiCall is a Gemini CLI tool call as caveira's, where caveira has
// one. Gemini has named some arguments two ways over its versions.
func geminiCall(id, name string, in map[string]any) llm.ToolCall {
	switch name {
	case "run_shell_command":
		return call(id, "bash", rename(in, "command", "description"))
	case "read_file":
		return call(id, "read_file", rename(in, "absolute_path:path", "file_path:path", "offset", "limit"))
	case "write_file":
		return call(id, "write_file", rename(in, "file_path:path", "content"))
	case "replace":
		return call(id, "edit_file", rename(in, "file_path:path", "old_string", "new_string"))
	case "glob":
		return call(id, "glob", rename(in, "pattern", "path", "dir_path:path"))
	case "search_file_content", "grep_search":
		return call(id, "grep", rename(in, "pattern", "path", "dir_path:path", "include:glob"))
	case "list_directory":
		return call(id, "list_dir", rename(in, "path", "dir_path:path"))
	}
	if in == nil {
		in = map[string]any{}
	}
	return call(id, name, in)
}
