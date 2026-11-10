package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestOpenLogWritesAndStartsOver(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, logName)
	if err := os.WriteFile(old, []byte(strings.Repeat("x", logMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		log.SetOutput(os.Stderr)
		debug.SetCrashOutput(nil, debug.CrashOptions{})
		closeLog()
	}()
	path := openLog(dir, "9.9.9")
	if path != old {
		t.Fatalf("path %q, want %q", path, old)
	}
	log.Printf("chat c1: turn failed: boom")
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "caveira 9.9.9 started") || !strings.Contains(string(b), "turn failed: boom") {
		t.Fatalf("log is %q", b)
	}
	if st, err := os.Stat(old + ".1"); err != nil || st.Size() != logMax+1 {
		t.Fatalf("the big log was not kept as .1: %v", err)
	}
}

func TestLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), logName)
	if got := logTail(path, 3); got != "" {
		t.Fatalf("no log gave %q", got)
	}
	os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0o600)
	if got := logTail(path, 3); got != "b\nc\nd" {
		t.Fatalf("tail %q", got)
	}
	if got := logTail(path, 10); got != "a\nb\nc\nd" {
		t.Fatalf("whole log %q", got)
	}
}

func TestReportURL(t *testing.T) {
	u := reportURL("0.3.1", "macOS 26.0", "/Users/x/.caveira/logs/desktop.log", "first line\nlast line")
	if !strings.HasPrefix(u, "mailto:contact@caveira.app?subject=caveira%200.3.1") {
		t.Fatalf("url %q", u)
	}
	if strings.Contains(u, "+") {
		t.Fatalf("a space became + in %q", u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	body := parsed.Query().Get("body")
	for _, want := range []string{"caveira 0.3.1 on macOS 26.0", "/Users/x/.caveira/logs/desktop.log", "first line\nlast line"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestReportURLKeepsTheNewestLinesUnderTheLimit(t *testing.T) {
	var lines []string
	for i := range 500 {
		lines = append(lines, fmt.Sprintf("line %03d: something went on here", i))
	}
	u := reportURL("0.3.1", "macOS 26.0", "/tmp/desktop.log", strings.Join(lines, "\n"))
	if len(u) > reportMax {
		t.Fatalf("url is %d long", len(u))
	}
	parsed, _ := url.Parse(u)
	body := parsed.Query().Get("body")
	if !strings.HasSuffix(body, "line 499: something went on here") || strings.Contains(body, "line 000") {
		t.Fatalf("kept the wrong end:\n%s", body[len(body)-200:])
	}
}

func TestReportProblemMailsTheLogAndShowsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), logName)
	os.WriteFile(path, []byte("started\npanic: boom\n"), 0o600)
	var mailed, shown string
	a := &App{version: "0.3.1", logPath: path}
	a.browse = func(u string) error { mailed = u; return nil }
	a.reveal = func(p string) error { shown = p; return nil }
	if err := a.reportProblem(); err != nil {
		t.Fatal(err)
	}
	if shown != path {
		t.Fatalf("showed %q", shown)
	}
	parsed, _ := url.Parse(mailed)
	if parsed.Opaque != "contact@caveira.app" || !strings.Contains(parsed.Query().Get("body"), "panic: boom") {
		t.Fatalf("mailed %q", mailed)
	}
}
