package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/justin06lee/caveira/core/update"
)

// updates is the release feed; tests point it at a fake one.
var updates = &update.Client{}

// executable is the binary to replace; tests point it elsewhere.
var executable = os.Executable

type updateAvailableMsg struct{ version string }

type updateDoneMsg struct {
	text string
	err  error
}

// updateEvery is how often a session looks for a newer release, so one that
// lands during a long session still gets mentioned.
const updateEvery = 6 * time.Hour

type updateTickMsg struct{}

func updateTick() tea.Cmd {
	return tea.Tick(updateEvery, func(time.Time) tea.Msg { return updateTickMsg{} })
}

// checkUpdate asks whether a newer release is out, at most every updateEvery.
func checkUpdate(version string) tea.Cmd {
	if !update.IsRelease(version) || update.Disabled() {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		latest, err := updates.Check(ctx, version, updateEvery)
		if err != nil || latest == "" {
			return nil
		}
		return updateAvailableMsg{version: latest}
	}
}

// updateLine is the quiet line that says a release is out.
func updateLine(version string) string {
	return "update available: caveira " + version + " · run /update"
}

func (m *Model) updateAvailable(msg updateAvailableMsg) {
	if m.newer == msg.version {
		return
	}
	m.newer = msg.version
	// With a transcript up the line goes into it; on the home screen it
	// sits under the tips, and pushHeader adds it under the header.
	if len(m.items) > 0 {
		m.push(&item{kind: itemNotice, text: updateLine(msg.version)})
	}
}

// SelfUpdate replaces the running binary with the latest release when it
// is newer than version, saying what it does on progress. The text that
// comes back is the last word: done, or already current.
func SelfUpdate(ctx context.Context, version string, progress func(string)) (string, error) {
	if err := update.Unsupported(); err != nil {
		return "", err
	}
	if !update.IsRelease(version) {
		return "", fmt.Errorf("this is a development build (%s); updates are for released versions — pull and run make instead", version)
	}
	rel, err := updates.Fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot check for updates: %w", err)
	}
	if !update.Newer(rel.Version, version) {
		return "caveira " + version + " is the latest", nil
	}
	exe, err := executable()
	if err != nil {
		return "", err
	}
	progress("Downloading caveira " + rel.Version + "…")
	if err := updates.ReplaceBinary(ctx, rel, exe); err != nil {
		var nw *update.NotWritableError
		if errors.As(err, &nw) {
			return "", err
		}
		return "", fmt.Errorf("update failed, caveira %s is still installed: %w", version, err)
	}
	update.AttemptedBinary(exe, rel.Version, version)
	return "Updated to caveira " + rel.Version + " · restart caveira to use it", nil
}

// runUpdate is /update: the download runs in the background and the
// session carries on; the new binary is used from the next start.
func (m *Model) runUpdate() tea.Cmd {
	if m.updating {
		m.push(&item{kind: itemNotice, text: "already updating"})
		return nil
	}
	m.updating = true
	version := m.version
	m.push(&item{kind: itemNotice, text: "checking for a newer caveira…"})
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		text, err := SelfUpdate(ctx, version, func(string) {})
		return updateDoneMsg{text: text, err: err}
	}
}

func (m *Model) updateDone(msg updateDoneMsg) {
	m.updating = false
	if msg.err != nil {
		m.push(&item{kind: itemError, text: msg.err.Error()})
	} else {
		m.newer = ""
		m.push(&item{kind: itemNotice, text: msg.text})
	}
	m.follow = true
}
