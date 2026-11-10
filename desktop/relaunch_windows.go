//go:build windows

package main

// relaunch is for the app updating itself, which it does not on Windows
// (see update.Unsupported): there is no bundle to open again, and bundle is
// "" there, so nothing calls this with anything.
func relaunch(bundle string) {}
