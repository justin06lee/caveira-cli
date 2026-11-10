package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/agent"
	"github.com/justin06lee/caveira/core/llm"
)

// Refresh brings an imported chat up to date with the one it came from,
// when the other app has carried that on since, saves it, and says
// whether anything changed. A copy carried on in caveira is left as it
// is: the two have gone separate ways. Only the original's own file is
// looked at, never the folder it ran in.
func Refresh(s *agent.Session, home string) (bool, error) {
	if s.Source == "" || (s.Origin != nil && s.Origin.Len != len(s.Messages)) {
		return false, nil
	}
	c, ok := locate(s, home)
	if !ok {
		return false, nil
	}
	since := s.UpdatedAt
	if s.Origin != nil {
		since = s.Origin.At
	}
	if !c.Updated.After(since) {
		return false, nil
	}
	fresh, err := Convert(c)
	if errors.Is(err, ErrEmpty) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A copy imported before origins were kept says nothing of how much
	// of it came over: it is the original's as long as the original
	// still starts with all of it.
	if s.Origin == nil && !startsWith(fresh.Messages, s.Messages) {
		s.Origin = &agent.Origin{Path: c.where, At: c.Updated, Len: -1}
		return false, s.Save()
	}
	grew := len(fresh.Messages) != len(s.Messages) || !startsWith(fresh.Messages, s.Messages)
	if grew {
		s.Messages = fresh.Messages
		s.UpdatedAt = fresh.UpdatedAt
	}
	s.Origin = fresh.Origin
	return grew, s.Save()
}

// locate finds the chat an imported session came from, and when it last
// changed.
func locate(s *agent.Session, home string) (Chat, bool) {
	var c Chat
	for _, app := range Apps {
		if id, ok := strings.CutPrefix(s.ID, app.ID+"-"); ok {
			c = Chat{App: app, ID: id, Dir: s.WorkDir}
			break
		}
	}
	if c.ID == "" {
		return c, false
	}
	if s.Origin != nil {
		c.where = s.Origin.Path
	}
	switch c.App {
	case OpenCode:
		if c.where == "" {
			c.where = openCodeDB(home)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var found []struct {
			Updated int64 `json:"time_updated"`
		}
		if rows(ctx, c.where, []string{"time_updated"}, "from session where id = "+sqlQuote(c.ID), &found) != nil || len(found) == 0 {
			return c, false
		}
		c.Updated = time.UnixMilli(found[0].Updated)
		return c, true
	case Claude:
		if c.where == "" {
			files, _ := filepath.Glob(filepath.Join(claudeProjects(home), "*", c.ID+".jsonl"))
			if len(files) > 0 {
				c.where = files[0]
			}
		}
	case Codex:
		if c.where == "" {
			// rollout-<started>-<id>.jsonl, in a folder for its day.
			_ = filepath.WalkDir(filepath.Join(codexHome(home), "sessions"), func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(p, "-"+c.ID+".jsonl") {
					c.where = p
					return filepath.SkipAll
				}
				return nil
			})
		}
	}
	st, err := os.Stat(c.where)
	if c.where == "" || err != nil {
		return c, false
	}
	c.Updated = st.ModTime()
	return c, true
}

// startsWith says whether msgs begins with every one of head.
func startsWith(msgs, head []llm.Message) bool {
	if len(head) > len(msgs) {
		return false
	}
	for i := range head {
		a, _ := json.Marshal(msgs[i])
		b, _ := json.Marshal(head[i])
		if !bytes.Equal(a, b) {
			return false
		}
	}
	return true
}
