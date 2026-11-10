//go:build windows

package proc

import (
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// jobs holds the job object each command runs in. A Windows process has no
// group to signal the way a Unix one does: what takes a command's children
// down with it is a job object, which the command is put into just after
// it starts, so everything it starts afterwards belongs to the job too.
var jobs sync.Map // *exec.Cmd -> *job

type job struct {
	mu sync.Mutex
	h  windows.Handle
}

func (j *job) terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.h, 1)
}

// Setup hides the console window the command would open (the desktop app
// has no console, so Windows would give each command its own) and makes
// cancelling it end the command and all it started.
func Setup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	j := &job{}
	jobs.Store(cmd, j)
	cmd.Cancel = func() error {
		// Without a job (it could not be made) only the shell itself goes.
		if err := j.terminate(); err != nil || j.h == 0 {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// Adopt puts the started command in its job. A child it starts in the
// instant before this is not covered; the shell takes longer than that to
// start anything.
func Adopt(cmd *exec.Cmd) {
	v, ok := jobs.Load(cmd)
	if !ok || cmd.Process == nil {
		return
	}
	j := v.(*job)
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(h)
		return
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		windows.CloseHandle(h)
		return
	}
	j.mu.Lock()
	j.h = h
	j.mu.Unlock()
}

// Release lets go of the job once the command has been waited for. What
// the command left running stays running, as it does on a Mac.
func Release(cmd *exec.Cmd) {
	v, ok := jobs.LoadAndDelete(cmd)
	if !ok {
		return
	}
	j := v.(*job)
	j.mu.Lock()
	if j.h != 0 {
		windows.CloseHandle(j.h)
		j.h = 0
	}
	j.mu.Unlock()
}

// Hide keeps the console window of a program started from the desktop app
// from flashing up.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// Signal is never the way a command ends on Windows.
func Signal(state interface{ Sys() any }) (string, bool) { return "", false }
