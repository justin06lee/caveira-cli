//go:build !windows

package tools

func windowsShell() Shell { return Shell{Name: "sh", Path: "sh", Kind: "posix"} }
