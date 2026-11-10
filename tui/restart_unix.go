//go:build !windows

package main

import (
	"os"
	"syscall"

	"github.com/justin06lee/caveira/core/update"
)

// startUpdated replaces this process with the binary at target. It returns
// only if that fails.
func startUpdated(target string) {
	_ = syscall.Exec(target, os.Args, append(os.Environ(), update.UpdatedFromEnv+"="+version))
}
