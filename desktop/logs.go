package main

import (
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"

	"github.com/justin06lee/caveira/core/config"
)

// The app keeps a log in ~/.caveira/logs/desktop.log: when it started,
// turns and updates that failed, and a crash's stack, which Go writes
// there on its way down. Report a problem mails the end of it to caveira;
// nothing is sent anywhere on its own.

const (
	logName = "desktop.log"
	// logMax is how big the log grows before the next launch starts a new
	// one, keeping the last beside it as desktop.log.1.
	logMax   = 1 << 20
	reportTo = "contact@caveira.app"
	// reportLines is how much of the log's end goes into the mail, and
	// reportMax how long the mailto link may grow: past a few thousand
	// characters some mail apps cut it off.
	reportLines = 200
	reportMax   = 8000
)

// logFile is the open log, which lives as long as the app does; the tests
// close it so that Windows lets their folders go.
var logFile *os.File

func closeLog() {
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

// tee writes the log to its file, and to stderr as well where there is
// one. The Windows app has no console, so its stderr is not a handle at
// all and writing to it fails; io.MultiWriter would then stop before the
// file and the log would stay empty.
type tee struct{ f *os.File }

func (t *tee) Write(p []byte) (int, error) {
	n, err := t.f.Write(p)
	_, _ = os.Stderr.Write(p)
	return n, err
}

func logDir() string { return filepath.Join(config.Dir(), "logs") }

// openLog sends the standard logger, and a crash's output, to the log in
// dir as well as stderr. It answers the log's path, or "" when it could
// not be opened.
func openLog(dir, version string) string {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, logName)
	if st, err := os.Stat(path); err == nil && st.Size() > logMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ""
	}
	logFile = f
	log.SetOutput(&tee{f})
	// An unhandled panic or fatal error goes to stderr and the file.
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	log.Printf("caveira %s started on %s (%s/%s)", version, osVersion(), goruntime.GOOS, goruntime.GOARCH)
	return path
}

// osVersion names the system, "macOS 26.0" on a Mac.
func osVersion() string {
	if goruntime.GOOS != "darwin" {
		return goruntime.GOOS
	}
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "macOS"
	}
	return "macOS " + strings.TrimSpace(string(out))
}

// logTail is the last n lines of the log at path, "" when there is none.
func logTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// reportURL is a mailto link to caveira about a problem: room to say what
// happened, the version and system, and as much of tail, newest last, as
// keeps the link under reportMax.
func reportURL(version, system, logPath, tail string) string {
	subject := "caveira " + version + ": a problem"
	head := "What happened, and what did you expect?\n\n\n\n---\n" +
		"caveira " + version + " on " + system + "\n" +
		"The whole log is " + logPath + "; attach it if you can.\n\n" +
		"The end of the log:\n"
	for {
		u := "mailto:" + reportTo + "?subject=" + mailEscape(subject) + "&body=" + mailEscape(head+tail)
		if len(u) <= reportMax || tail == "" {
			return u
		}
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = ""
		}
	}
}

// mailEscape escapes s for a mailto link, where a space is %20, not +.
func mailEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// reportProblem opens a mail to caveira with the end of the log in it, and
// shows the log in the Finder to attach, since a mailto link can't carry a
// file.
func (a *App) reportProblem() error {
	path := a.logPath
	if path == "" {
		path = filepath.Join(logDir(), logName)
	}
	tail := logTail(path, reportLines)
	if tail != "" && a.reveal != nil {
		_ = a.reveal(path)
	}
	return a.browse(reportURL(a.version, osVersion(), path, tail))
}

// revealInFinder shows path selected in a Finder window.
func revealInFinder(path string) error {
	if goruntime.GOOS != "darwin" {
		return nil
	}
	return exec.Command("open", "-R", path).Start()
}
