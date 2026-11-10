package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/llm"
)

func TestGemini(t *testing.T) {
	home, project := fakeHome(t)
	root := filepath.Join(home, ".gemini")
	b, _ := json.Marshal(obj{"projects": obj{project: "proj"}})
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "projects.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	msg := func(id, typ string, content any, extra obj) obj {
		m := obj{"id": id, "timestamp": "2026-10-01T10:00:00.000Z", "type": typ, "content": content}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	context0 := msg("c0", "user", []obj{{"text": "<session_context>\nThis is the Gemini CLI.\n</session_context>"}}, nil)
	shell := obj{"id": "t1", "name": "run_shell_command", "args": obj{"command": "make", "description": "build"}, "status": "executing"}
	chats := filepath.Join(root, "tmp", "proj", "chats")
	writeLines(t, filepath.Join(chats, "session-2026-10-01T10-00-g1.jsonl"),
		obj{"sessionId": "g1", "projectHash": "x", "startTime": "2026-10-01T10:00:00.000Z", "kind": "main"},
		obj{"$set": obj{"messages": []obj{context0}}},
		msg("u1", "user", []obj{{"text": "fix the build"}}, nil),
		// A reply is written again whole as its calls finish.
		msg("a1", "gemini", "Looking.", obj{"toolCalls": []obj{shell}}),
		msg("a1", "gemini", "Looking.", obj{
			"thoughts": []obj{{"subject": "Reading", "description": "the Makefile"}},
			"toolCalls": []obj{
				{"id": "t1", "name": "run_shell_command", "args": obj{"command": "make", "description": "build"}, "status": "success",
					"result": []obj{{"functionResponse": obj{"id": "t1", "name": "run_shell_command", "response": obj{"output": "ok"}}}}},
				{"id": "t2", "name": "replace", "args": obj{"file_path": "/p/Makefile", "old_string": "a", "new_string": "b"}, "status": "success",
					"result":        []obj{{"functionResponse": obj{"response": obj{"output": "Replaced 1 occurrence."}}}},
					"resultDisplay": obj{"fileName": "Makefile", "fileDiff": "Index: Makefile\n===\n--- Makefile\n+++ Makefile\n@@ -1 +1 @@\n-a\n+b\n"}},
				{"id": "t3", "name": "read_file", "args": obj{"absolute_path": "/p/missing"}, "status": "error",
					"result": []obj{{"functionResponse": obj{"response": obj{"error": "no such file"}}}}},
			}}),
		obj{"$set": obj{"lastUpdated": "2026-10-01T10:01:00.000Z"}},
		msg("i1", "info", "Switched model.", nil),
		msg("a2", "gemini", []obj{{"text": "Done."}}, nil),
	)
	// A subagent's chat is part of the one that started it.
	writeLines(t, filepath.Join(chats, "session-2026-10-01T10-05-g2.jsonl"),
		obj{"sessionId": "g2", "kind": "subagent"}, msg("u1", "user", "look around", nil))
	// Older versions wrote one JSON record, in a folder named by the
	// project path's hash.
	sum := sha256.Sum256([]byte(project))
	old, _ := json.Marshal(obj{"sessionId": "g3", "projectHash": hex.EncodeToString(sum[:]),
		"messages": []obj{msg("u1", "user", "hello", nil), msg("a1", "gemini", "hi", nil)}})
	oldPath := filepath.Join(root, "tmp", hex.EncodeToString(sum[:]), "chats", "session-2025-07-01T10-00-g3.json")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, old, 0o644); err != nil {
		t.Fatal(err)
	}

	found := map[string]Chat{}
	for _, c := range Scan(context.Background(), home) {
		if c.App == Gemini {
			found[c.ID] = c
		}
	}
	if len(found) != 2 || found["g1"].Dir != project || found["g3"].Dir != project {
		t.Fatalf("want g1 and g3 in %s, found %+v", project, found)
	}

	s, err := Convert(found["g1"])
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "gemini-g1" || s.Source != "Gemini CLI" || s.Title != "fix the build" || s.WorkDir != project {
		t.Errorf("header: %+v", s)
	}
	m := s.Messages
	if len(m) != 6 {
		for _, x := range m {
			t.Logf("%s %q %+v", x.Role, x.Content, x.ToolCalls)
		}
		t.Fatalf("want 6 messages, got %d", len(m))
	}
	if m[0].Role != llm.RoleUser || m[0].Content != "fix the build" {
		t.Errorf("the session context is left out: %+v", m[0])
	}
	c := m[1].ToolCalls
	if m[1].Content != "Looking." || m[1].Reasoning != "Reading\n\nthe Makefile" || len(c) != 3 {
		t.Fatalf("the reply as last written: %+v", m[1])
	}
	if c[0].Function.Name != "bash" || args(t, c[0])["command"] != "make" ||
		c[1].Function.Name != "edit_file" || args(t, c[1])["path"] != "/p/Makefile" ||
		c[2].Function.Name != "read_file" || args(t, c[2])["path"] != "/p/missing" {
		t.Errorf("calls as caveira's: %+v", c)
	}
	if m[2].Content != "ok" || m[3].Content != "Edited /p/Makefile.\n\n@@ -1 +1 @@\n-a\n+b" || m[4].Content != "Error: no such file" {
		t.Errorf("results: %q %q %q", m[2].Content, m[3].Content, m[4].Content)
	}
	if m[5].Role != llm.RoleAssistant || m[5].Content != "Done." {
		t.Errorf("last reply: %+v", m[5])
	}

	if s, err := Convert(found["g3"]); err != nil || len(s.Messages) != 2 || s.Messages[1].Content != "hi" {
		t.Errorf("older chat: %+v, %v", s, err)
	}

	// Carried on in Gemini, the chat catches up when refreshed.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(found["g1"].where, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	more, _ := json.Marshal(msg("u2", "user", "and the tests", nil))
	f.Write(append(more, '\n'))
	f.Close()
	later := time.Now().Add(time.Minute)
	os.Chtimes(found["g1"].where, later, later)
	if grew, err := Refresh(s, home); err != nil || !grew || s.Messages[len(s.Messages)-1].Content != "and the tests" {
		t.Errorf("refresh: grew %v, err %v, last %+v", grew, err, s.Messages[len(s.Messages)-1])
	}
}
