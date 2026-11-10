//go:build windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func lookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	return p, err == nil
}

func windowsShell() Shell {
	if p := gitBash(); p != "" {
		return Shell{Name: "bash (Git Bash)", Path: p, Kind: "posix"}
	}
	if p, err := exec.LookPath("pwsh"); err == nil {
		return Shell{Name: "PowerShell 7", Path: p, Kind: "powershell"}
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return Shell{Name: "PowerShell", Path: p, Kind: "powershell"}
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return Shell{Name: "PowerShell", Path: filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), Kind: "powershell"}
}

// gitBash is the bash that comes with Git for Windows, or "". It is looked
// for beside the git on PATH and in the places the installer uses, not by
// asking PATH for bash: that finds C:\Windows\System32\bash.exe, which
// starts the Windows Subsystem for Linux.
func gitBash() string {
	var roots []string
	if g, err := exec.LookPath("git"); err == nil {
		// …\Git\cmd\git.exe or …\Git\bin\git.exe, …\Git\mingw64\bin\git.exe
		d := filepath.Dir(g)
		roots = append(roots, filepath.Dir(d), filepath.Dir(filepath.Dir(d)))
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if v := os.Getenv(env); v != "" {
			roots = append(roots, filepath.Join(v, "Git"))
		}
	}
	if v := os.Getenv("LocalAppData"); v != "" {
		roots = append(roots, filepath.Join(v, "Programs", "Git"))
	}
	for _, r := range roots {
		p := filepath.Join(r, "bin", "bash.exe")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && !strings.Contains(strings.ToLower(p), `\windows\system32\`) {
			return p
		}
	}
	return ""
}
