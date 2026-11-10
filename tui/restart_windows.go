//go:build windows

package main

// startUpdated is for the launch update, which is off on Windows (see
// update.Unsupported): a program there cannot replace itself the way
// syscall.Exec does on Unix, and nothing is ever installed to start. It
// returns at once, which callers take as "it did not start".
func startUpdated(target string) {}
