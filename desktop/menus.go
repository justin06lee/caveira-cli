package main

import (
	"context"
	"log"
	goruntime "runtime"
	"time"

	"github.com/justin06lee/caveira/core/update"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The Mac menu bar. Wails' stock app menu takes no items of its own, so
// the caveira menu is built here with what it had (About, Hide, Hide
// Others, Show All, Quit) and Settings….

const (
	aboutText = "A coding agent for open-weight models."
	helpURL   = "https://caveira.app"
)

func appMenu(app *App) *menu.Menu {
	emit := func(action string) menu.Callback {
		return func(*menu.CallbackData) { app.emit("menu", action) }
	}
	bar := menu.NewMenu()
	caveira := bar.AddSubmenu("caveira")
	caveira.AddText("About caveira", nil, func(*menu.CallbackData) { app.showAbout() })
	caveira.AddSeparator()
	caveira.AddText("Settings…", keys.CmdOrCtrl(","), emit("settings"))
	caveira.AddSeparator()
	caveira.AddText("Hide caveira", keys.CmdOrCtrl("h"), func(*menu.CallbackData) { app.hide() })
	caveira.AddText("Hide Others", keys.Combo("h", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(*menu.CallbackData) { hideOthers() })
	caveira.AddText("Show All", nil, func(*menu.CallbackData) { showAll() })
	caveira.AddSeparator()
	caveira.AddText("Quit caveira", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { app.quit() })

	file := bar.AddSubmenu("File")
	file.AddText("New Chat", keys.CmdOrCtrl("n"), emit("new-chat"))
	file.AddText("Open Project…", keys.CmdOrCtrl("o"), emit("open-project"))
	file.AddSeparator()
	file.AddText("Import from Other Agents…", nil, emit("import"))
	bar.Append(menu.EditMenu())
	view := bar.AddSubmenu("View")
	view.AddText("Toggle Sidebar", keys.CmdOrCtrl("\\"), emit("toggle-sidebar"))
	bar.Append(menu.WindowMenu())

	help := bar.AddSubmenu("Help")
	help.AddText("caveira Help", nil, func(*menu.CallbackData) { app.browse(helpURL) })
	help.AddText("Report a Problem…", nil, func(*menu.CallbackData) {
		if err := app.reportProblem(); err != nil {
			log.Printf("report a problem: %v", err)
		}
	})
	help.AddSeparator()
	help.AddText("Check for Updates…", nil, func(*menu.CallbackData) { app.checkForUpdates() })
	return bar
}

func (a *App) showAbout() {
	if a.ctx == nil {
		return
	}
	runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type: runtime.InfoDialog, Title: "caveira", Message: aboutText + "\n" + a.version, Icon: icon,
	})
}

func (a *App) hide() {
	if a.ctx != nil {
		runtime.Hide(a.ctx)
	}
}

func (a *App) quit() {
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

// updatable says whether this copy of the app can update itself: a
// release build, in a Mac app bundle, with checks on.
func (a *App) updatable() bool {
	return update.IsRelease(a.version) && goruntime.GOOS == "darwin" && update.Bundle(executablePath()) != ""
}

// updateAnswer is what Check for Updates… says: a title, a message, and
// whether to offer to install latest.
func updateAnswer(version string, updatable, disabled bool, latest string, err error) (title, message string, offer bool) {
	switch {
	case !updatable:
		return "caveira " + version, "This copy of caveira was built from source, so it doesn't update itself.", false
	case disabled:
		return "Update checks are off", "Turn them back on in ~/.caveira/config.json (no_update_check) or by unsetting CAVEIRA_NO_UPDATE_CHECK.", false
	case err != nil:
		return "Couldn't check for updates", err.Error(), false
	case latest == "":
		return "You're up to date", "caveira " + version + " is the latest version.", false
	}
	return "caveira " + latest + " is out", "You have " + version + ". Install " + latest + " now? caveira opens again when it's done.", true
}

// checkForUpdates asks for the latest release now, however recently it
// was asked, and says what it found. A newer release also shows the
// window's update button, as the six-hourly check does.
func (a *App) checkForUpdates() {
	if a.ctx == nil {
		return
	}
	var latest string
	var err error
	if a.updatable() && !update.Disabled() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		latest, err = a.updates.Check(ctx, a.version, 0)
		cancel()
		if err != nil {
			log.Printf("update check: %v", err)
		}
	}
	if latest != "" {
		a.mu.Lock()
		a.update.Version = latest
		v := a.update
		a.mu.Unlock()
		a.emit("update", v)
	}
	title, message, offer := updateAnswer(a.version, a.updatable(), update.Disabled(), latest, err)
	opts := runtime.MessageDialogOptions{Type: runtime.InfoDialog, Title: title, Message: message, Icon: icon}
	if offer {
		opts.Type, opts.Buttons, opts.DefaultButton, opts.CancelButton = runtime.QuestionDialog, []string{"Install", "Later"}, "Install", "Later"
	}
	if choice, _ := runtime.MessageDialog(a.ctx, opts); offer && choice == "Install" {
		if err := a.InstallUpdate(); err != nil {
			runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{Type: runtime.WarningDialog, Title: "The update didn't install", Message: err.Error()})
		}
	}
}
