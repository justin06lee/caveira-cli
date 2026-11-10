package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What the bash tool does on Windows, in each shell it can have: Git Bash
// when it is installed, Windows PowerShell, and PowerShell 7 when that is
// installed. The same ground in each: output, both streams, the exit code,
// the working directory, UTF-8, a timeout, and a timeout that takes the
// command's children with it.

type shellCase struct {
	name string
	path string // the shell, for CAVEIRA_SHELL
	// the commands for this shell
	out, pwd, sleep, late, stale, uni, crlf string
}

func windowsShells(t *testing.T) []shellCase {
	t.Helper()
	var out []shellCase
	ps := func(name, path string) shellCase {
		return shellCase{
			name:  name,
			path:  path,
			out:   `Write-Output 'hi'; [Console]::Error.WriteLine('err'); exit 3`,
			pwd:   `(Get-Location).Path`,
			sleep: `Start-Sleep 20`,
			// cmd.exe is started by the shell and starts ping: a tree three deep.
			late:  `cmd /c "ping -n 6 127.0.0.1 >nul & echo late> late.txt"`,
			stale: `cmd /c exit 7; Get-Date | Out-Null`,
			uni:   `$x='héllo ✓ 日本'; Write-Output $x`,
			crlf:  `cmd /c "echo a& echo b"`,
		}
	}
	if p := gitBash(); p != "" {
		out = append(out, shellCase{
			name: "git bash", path: p,
			out:   `echo hi; echo err >&2; exit 3`,
			pwd:   `pwd -W`,
			sleep: `sleep 20`,
			late:  `(sleep 5; echo late > late.txt) & wait`,
			stale: `(exit 7); true`,
			uni:   `x='héllo ✓ 日本'; echo $x`,
			crlf:  `printf 'a\r\nb\r\n' | cat`,
		})
	}
	if p, ok := lookPath("powershell"); ok {
		out = append(out, ps("powershell", p))
	}
	if p, ok := lookPath("pwsh"); ok {
		out = append(out, ps("pwsh", p))
	}
	if len(out) == 0 {
		t.Fatal("no shell found on a Windows machine")
	}
	return out
}

func run(t *testing.T, reg *Registry, cmd string, timeout int) Result {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"command": cmd, "timeout": timeout})
	if timeout == 0 {
		b, _ = json.Marshal(map[string]any{"command": cmd})
	}
	return reg.Run(context.Background(), "bash", b)
}

func TestShellsOnWindows(t *testing.T) {
	for _, sc := range windowsShells(t) {
		t.Run(sc.name, func(t *testing.T) {
			t.Setenv(ShellOverride, sc.path)
			t.Logf("shell %+v", DetectShell())
			dir := t.TempDir()
			reg := Default(dir)

			r := run(t, reg, sc.out, 0)
			if !r.IsError || !strings.Contains(r.Output, "hi") || !strings.Contains(r.Output, "err") || !strings.Contains(r.Output, "[exit code 3]") {
				t.Errorf("output and exit code: %q", r.Output)
			}

			r = run(t, reg, sc.pwd, 0)
			got, _ := filepath.EvalSymlinks(strings.TrimSpace(strings.Split(r.Output, "\n")[0]))
			want, _ := filepath.EvalSymlinks(dir)
			if !strings.EqualFold(got, want) {
				t.Errorf("working directory %q, want %q (%q)", got, want, r.Output)
			}

			r = run(t, reg, sc.stale, 0)
			if r.IsError {
				t.Errorf("an old failure counted against a later success: %q", r.Output)
			}

			// Programs end lines with \r\n; the model and the screen get \n.
			r = run(t, reg, sc.crlf, 0)
			if strings.Contains(r.Output, "\r") || !strings.HasPrefix(r.Output, "a\nb") {
				t.Errorf("CRLF output was not normalized: %q", r.Output)
			}

			r = run(t, reg, sc.uni, 0)
			if !strings.Contains(r.Output, "héllo ✓ 日本") {
				t.Errorf("UTF-8 did not survive: %q", r.Output)
			}

			start := time.Now()
			r = run(t, reg, sc.sleep, 1)
			if !r.IsError || !strings.Contains(r.Output, "timed out") || time.Since(start) > 10*time.Second {
				t.Errorf("timeout after %v: %q", time.Since(start), r.Output)
			}

			// Killed on a timeout, the command's children go with it: the
			// file they would write a few seconds on is never written.
			os.Remove(filepath.Join(dir, "late.txt"))
			r = run(t, reg, sc.late, 1)
			if !strings.Contains(r.Output, "timed out") {
				t.Errorf("no timeout: %q", r.Output)
			}
			time.Sleep(8 * time.Second)
			if _, err := os.Stat(filepath.Join(dir, "late.txt")); err == nil {
				t.Error("a child outlived its command's timeout")
			}
			// The control: left alone it does write the file.
			r = run(t, reg, sc.late, 30)
			if _, err := os.Stat(filepath.Join(dir, "late.txt")); err != nil {
				t.Errorf("the control command did not write its file: %q", r.Output)
			}
		})
	}
}

func TestPowerShellExitCodes(t *testing.T) {
	var ps shellCase
	for _, sc := range windowsShells(t) {
		if sc.name != "git bash" {
			ps = sc
			break
		}
	}
	if ps.path == "" {
		t.Skip("no PowerShell")
	}
	t.Setenv(ShellOverride, ps.path)
	reg := Default(t.TempDir())
	for cmd, want := range map[string]string{
		`cmd /c exit 7`:                "[exit code 7]",
		`Get-Item .\no-such-file-here`: "[exit code 1]",
		`Get-Date | Out-Null`:          "no output, exit code 0",
		`throw "boom"`:                 "boom",
	} {
		r := run(t, reg, cmd, 0)
		if !strings.Contains(r.Output, want) {
			t.Errorf("%s: %q, want %q", cmd, r.Output, want)
		}
	}
}

// Without an override, Git Bash is used when there, else PowerShell, and the
// Windows Subsystem for Linux launcher is never picked.
func TestDetectShellOnWindows(t *testing.T) {
	t.Setenv(ShellOverride, "")
	s := DetectShell()
	t.Logf("detected %+v", s)
	if strings.Contains(strings.ToLower(s.Path), `\windows\system32\bash.exe`) {
		t.Errorf("picked the WSL launcher: %+v", s)
	}
	if p := gitBash(); p != "" {
		if s.Path != p || s.Kind != "posix" {
			t.Errorf("Git Bash is at %s but %+v was picked", p, s)
		}
	} else if s.Kind != "powershell" {
		t.Errorf("no Git Bash, and %+v was picked", s)
	}
}
