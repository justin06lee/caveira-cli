package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/importer"
)

// An imported chat that went on in Claude Code since opens with what
// was said there.
func TestImportedChatCatchesUpWhenOpened(t *testing.T) {
	a, dir := testApp(t, nil)
	home, _ := os.UserHomeDir()
	file := filepath.Join(home, ".claude", "projects", "-proj", "s1.jsonl")
	var lines []string
	say := func(uuid, parent, role, text string, age time.Duration) {
		b, _ := json.Marshal(map[string]any{"type": role, "uuid": uuid, "parentUuid": parent, "cwd": dir,
			"timestamp": "2026-09-01T10:00:00Z",
			"message":   map[string]any{"id": uuid, "role": role, "content": []map[string]any{{"type": "text", "text": text}}}})
		lines = append(lines, string(b))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(age)
		_ = os.Chtimes(file, at, at)
	}
	say("u1", "", "user", "hello", -time.Hour)
	say("a1", "u1", "assistant", "hi", -time.Hour)
	old := importer.Scratch
	importer.Scratch = nil
	t.Cleanup(func() { importer.Scratch = old })
	var found []importer.Chat
	for _, c := range importer.Scan(context.Background(), home) {
		if c.App == importer.Claude {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d Claude Code chats", len(found))
	}
	s, err := importer.Convert(found[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	say("u2", "a1", "user", "and later?", -time.Minute)
	say("a2", "u2", "assistant", "said in Claude Code", -time.Minute)
	v, err := a.OpenChat(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, it := range v.Items {
		if it.Kind == "user" || it.Kind == "assistant" {
			said = append(said, it.Text)
		}
	}
	if strings.Join(said, "|") != "hello|hi|and later?|said in Claude Code" {
		t.Fatalf("opened with %q", said)
	}
}
