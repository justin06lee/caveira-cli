package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Shell is the program the bash tool runs a command with.
type Shell struct {
	// Name is what the model is told it is: "bash", "sh", "PowerShell",
	// "PowerShell 7", "cmd".
	Name string
	// Path is the program.
	Path string
	// Kind is the family of syntax: "posix", "powershell" or "cmd".
	Kind string
}

// Args is the arguments that make the shell run one command and exit.
func (s Shell) Args(command string) []string {
	switch s.Kind {
	case "powershell":
		return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powershellScript(command)}
	case "cmd":
		return []string{"/d", "/s", "/c", command}
	}
	return []string{"-c", command}
}

// powershellScript wraps a command so that it runs the way a shell command
// is expected to: output as UTF-8 whatever the console's code page is, no
// progress bars, and the exit status of the last command as the process's,
// which -Command alone reduces to 0 or 1.
func powershellScript(command string) string {
	return "$ProgressPreference='SilentlyContinue'; " +
		"[Console]::OutputEncoding=(New-Object System.Text.UTF8Encoding $false); $OutputEncoding=[Console]::OutputEncoding\n" +
		command +
		"\nif (-not $?) { if ($LASTEXITCODE) { exit $LASTEXITCODE } else { exit 1 } }"
}

// ShellOverride is the environment variable that picks the shell: bash,
// sh, powershell, pwsh, cmd, or the path of a program.
const ShellOverride = "CAVEIRA_SHELL"

// DetectShell is the shell commands run in on this machine. On a Mac or
// Linux it is bash, or sh when there is no bash. On Windows it is Git Bash
// when it is installed, because the models write bash best, and
// PowerShell otherwise, which every Windows has. The Windows Subsystem for
// Linux is not picked on its own: its files are not at the paths the model
// is told about.
func DetectShell() Shell {
	if v := strings.TrimSpace(os.Getenv(ShellOverride)); v != "" {
		if s, ok := shellNamed(v); ok {
			return s
		}
	}
	if runtime.GOOS == "windows" {
		return windowsShell()
	}
	for _, name := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			return Shell{Name: name, Path: p, Kind: "posix"}
		}
	}
	return Shell{Name: "sh", Path: "sh", Kind: "posix"}
}

// shellNamed resolves the override: a program by name or by path.
func shellNamed(v string) (Shell, bool) {
	p, err := exec.LookPath(v)
	if err != nil {
		return Shell{}, false
	}
	base := strings.ToLower(strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".exe"))
	switch base {
	case "powershell":
		return Shell{Name: "PowerShell", Path: p, Kind: "powershell"}, true
	case "pwsh":
		return Shell{Name: "PowerShell 7", Path: p, Kind: "powershell"}, true
	case "cmd":
		return Shell{Name: "cmd", Path: p, Kind: "cmd"}, true
	}
	return Shell{Name: base, Path: p, Kind: "posix"}, true
}

// printsText says a command's first word is one of the
// shell's ways to print text.
func printsText(name string) bool {
	switch strings.ToLower(name) {
	case "echo", "printf", "write-host", "write-output":
		return true
	}
	return false
}
