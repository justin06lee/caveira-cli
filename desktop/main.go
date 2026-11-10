// Command caveira-desktop is caveira as a desktop app: the same agent as
// the terminal client, in a window, built with Wails.
package main

import (
	"context"
	"embed"
	"github.com/justin06lee/caveira/core/update"
	"log"
	"os"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

// version is set by the Makefile from the git describe output.
var version = "dev"

func main() {
	// A version that failed to come up last time goes back to the one it
	// replaced before any window opens; see core/update/launch.go.
	if update.IsRelease(version) {
		if bundle := update.Bundle(executablePath()); bundle != "" &&
			update.Started(update.App, version) && update.RollBack(update.App, bundle) {
			relaunch(bundle)
			return
		}
	}
	// A Finder launch starts with a bare PATH; the user's login shell knows
	// where go, bun, and the rest live. Loaded while the window opens.
	go loadShellEnv()

	// WebKitGTK's DMA-BUF renderer paints a blank window under Xvfb and on
	// NVIDIA's drivers, Jetson included; the shared-memory path works
	// everywhere.
	if goruntime.GOOS == "linux" && os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}

	app := NewApp(version)
	app.logPath = openLog(logDir(), version)

	appMenu := appMenu(app)
	// On Linux the menu would be a GTK menu bar across the top of the
	// window; the page takes the same shortcuts instead (App.tsx), and
	// Import is in Settings.
	if goruntime.GOOS != "darwin" {
		appMenu = nil
	}

	err := wails.Run(&options.App{
		Title:     "caveira",
		Width:     1180,
		Height:    780,
		MinWidth:  720,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Menu:             appMenu,
		BackgroundColour: &options.RGBA{R: 248, G: 247, B: 242, A: 255},
		OnStartup:        app.startup,
		OnBeforeClose: func(ctx context.Context) bool {
			app.stopAll()
			return false
		},
		Bind: []any{app},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHidden(),
			Appearance:           mac.DefaultAppearance,
			WebviewIsTransparent: false,
		},
		// The window's WM_CLASS is the binary's name, caveira-desktop once
		// installed, which the launcher's StartupWMClass names: GTK has set
		// the program name before Wails' ProgramName could change it.
		Linux: &linux.Options{
			Icon: icon,
			// What Wails picks when Linux options are left out: compositing
			// on the GPU leaves the window blank on some NVIDIA setups.
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
