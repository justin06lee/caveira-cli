//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// Setup gives cmd a process group of its own, which cancelling the command
// kills whole, so a timeout takes the command's children with it.
func Setup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Adopt is called right after cmd has started. Nothing is needed here.
func Adopt(cmd *exec.Cmd) {}

// Release is called once cmd has been waited for.
func Release(cmd *exec.Cmd) {}

// Hide keeps a window from opening for cmd. Nothing opens one here.
func Hide(cmd *exec.Cmd) {}

// Signal says how the process ended, when a signal ended it.
func Signal(state interface{ Sys() any }) (string, bool) {
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return ws.Signal().String(), true
}
