//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// relaunch opens bundle again once this process has gone. Only the Mac app
// updates itself, so this runs only there.
func relaunch(bundle string) {
	pid := strconv.Itoa(os.Getpid())
	cmd := exec.Command("/bin/sh", "-c", `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; open "$2"`, "relaunch", pid, bundle)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Start()
}
