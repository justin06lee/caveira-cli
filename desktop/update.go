package main

import (
	"context"
	"errors"
	"log"
	"os"
	goruntime "runtime"
	"time"

	"github.com/justin06lee/caveira/core/update"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The app installs a newer release as it opens (launchUpdate) and looks for
// one every six hours after, which it mentions with a quiet button. Only the
// macOS app updates itself: the release carries it as caveira.dmg.

const updateEvery = 6 * time.Hour

// UpdateView is what the window shows about updates.
type UpdateView struct {
	// Version is a newer release, or "" when there is none.
	Version string `json:"version"`
	// Installing says the update is being downloaded and put in place.
	Installing bool `json:"installing"`
	// Ready says it is in place and restarting will start it.
	Ready bool `json:"ready"`
}

// watchUpdates checks now and every updateEvery, until ctx ends.
func (a *App) watchUpdates(ctx context.Context) {
	if !update.IsRelease(a.version) || goruntime.GOOS != "darwin" || update.Bundle(executablePath()) == "" {
		return
	}
	a.launchUpdate(ctx)
	for {
		check, cancel := context.WithTimeout(ctx, 30*time.Second)
		latest, err := a.updates.Check(check, a.version, updateEvery)
		cancel()
		if err != nil {
			log.Printf("update check: %v", err)
		}
		if err == nil && latest != "" {
			a.mu.Lock()
			changed := a.update.Version != latest
			a.update.Version = latest
			v := a.update
			a.mu.Unlock()
			if changed {
				a.emit("update", v)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateEvery):
		}
	}
}

// launchUpdate installs a newer release as the app opens, on the goroutine
// of watchUpdates, so the window is up and usable meanwhile. Anything that
// goes wrong leaves this version running, with the Update button. The new
// app goes in without touching what is running: the window restarts into
// it only when no chat is going and nothing is half-typed (store.ts), and
// otherwise offers Restart, and it starts anyway on the next launch.
func (a *App) launchUpdate(ctx context.Context) {
	if update.AutoDisabled() {
		return
	}
	bundle := update.Bundle(executablePath())
	if !update.CanReplaceApp(bundle) {
		return
	}
	check, cancel := context.WithTimeout(ctx, update.LaunchCheckTimeout)
	rel, err := a.updates.FetchLatest(check)
	cancel()
	if err != nil {
		log.Printf("update at launch: %v", err)
		return
	}
	if !update.Newer(rel.Version, a.version) || update.Skipped(update.App, rel.Version) {
		return
	}
	a.mu.Lock()
	if a.update.Installing {
		a.mu.Unlock()
		return
	}
	a.update = UpdateView{Version: rel.Version, Installing: true}
	v := a.update
	a.mu.Unlock()
	a.emit("update", v)

	get, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var cliErr *update.CLIError
	err = a.updates.InstallApp(get, rel, bundle)
	if err != nil && !errors.As(err, &cliErr) {
		log.Printf("update at launch: %v", err)
		a.mu.Lock()
		a.update = UpdateView{Version: rel.Version}
		v = a.update
		a.mu.Unlock()
		a.emit("update", v)
		return
	}
	update.Attempted(update.App, rel.Version, a.version, update.AppBackup(bundle))
	a.mu.Lock()
	a.update = UpdateView{Version: rel.Version, Ready: true}
	v = a.update
	a.mu.Unlock()
	a.emit("update", v)
}

// RestartForUpdate opens the new app that launchUpdate put in place and
// quits this one.
func (a *App) RestartForUpdate() {
	relaunch(update.Bundle(executablePath()))
	if a.ctx != nil {
		a.stopAll()
		runtime.Quit(a.ctx)
	}
}

func executablePath() string {
	exe, _ := os.Executable()
	return exe
}

// Update is what the window should show now. It is asked as the window
// loads, so it is also how a new version knows it got all the way up.
func (a *App) Update() UpdateView {
	update.Healthy(update.App, a.version)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.update
}

// InstallUpdate downloads the latest release's disk image, checks it,
// puts the app in place of this one and the terminal client in place of
// one install.sh put in ~/.local/bin, then opens the new app and quits
// this one. Where the app's folder is not writable the disk image opens
// in the Finder instead, and the error says to drag it across.
func (a *App) InstallUpdate() (err error) {
	if err := update.Unsupported(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			log.Printf("update: %v", err)
		}
	}()
	a.mu.Lock()
	if a.update.Installing {
		a.mu.Unlock()
		return errors.New("already updating")
	}
	a.update.Installing = true
	a.mu.Unlock()
	a.emit("update", a.Update())
	defer func() {
		a.mu.Lock()
		a.update.Installing = false
		v := a.update
		a.mu.Unlock()
		a.emit("update", v)
	}()

	bundle := update.Bundle(executablePath())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rel, err := a.updates.Fetch(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Version, a.version) {
		return errors.New("caveira " + a.version + " is the latest")
	}
	// The app in place with the terminal client left behind is still an
	// update: the new app opens, and the client can update itself.
	var cliErr *update.CLIError
	if err := a.updates.InstallApp(ctx, rel, bundle); err != nil && !errors.As(err, &cliErr) {
		return err
	}
	update.Attempted(update.App, rel.Version, a.version, update.AppBackup(bundle))
	relaunch(bundle)
	if a.ctx != nil {
		a.stopAll()
		runtime.Quit(a.ctx)
	}
	return nil
}
