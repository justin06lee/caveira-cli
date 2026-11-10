package tools

import (
	"runtime"
	"strings"
	"testing"
)

func TestShellArgs(t *testing.T) {
	if got := (Shell{Kind: "posix"}).Args("ls -l"); len(got) != 2 || got[0] != "-c" || got[1] != "ls -l" {
		t.Errorf("posix %q", got)
	}
	if got := (Shell{Kind: "cmd"}).Args("dir"); strings.Join(got, " ") != "/d /s /c dir" {
		t.Errorf("cmd %q", got)
	}
	ps := (Shell{Kind: "powershell"}).Args("Get-Date")
	last := ps[len(ps)-1]
	if ps[len(ps)-2] != "-Command" || !strings.Contains(last, "\nGet-Date\n") || !strings.Contains(last, "exit $LASTEXITCODE") {
		t.Errorf("powershell %q", ps)
	}
	for _, a := range ps[:len(ps)-1] {
		if strings.EqualFold(a, "-EncodedCommand") || strings.EqualFold(a, "-ExecutionPolicy") {
			t.Errorf("powershell is started with %s", a)
		}
	}
}

func TestShellOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is not on every Windows machine's PATH; shell_windows_test.go covers the shells there")
	}
	t.Setenv(ShellOverride, "sh")
	if s := DetectShell(); s.Kind != "posix" || !strings.HasSuffix(strings.TrimSuffix(s.Path, ".exe"), "sh") {
		t.Errorf("override sh gave %+v", s)
	}
	t.Setenv(ShellOverride, "no-such-shell-anywhere")
	if s := DetectShell(); s.Path == "" || strings.Contains(s.Path, "no-such") {
		t.Errorf("a shell that is not there gave %+v", s)
	}
}

func TestShellNameIsInTheDescription(t *testing.T) {
	d := (&bashTool{}).Description()
	if !strings.Contains(d, "Run a shell command") {
		t.Fatal(d)
	}
	if got := shellNote(Shell{Kind: "powershell", Name: "PowerShell"}); !strings.Contains(got, "PowerShell, not bash") {
		t.Errorf("note %q", got)
	}
}

func TestPrintsTextCoversPowerShell(t *testing.T) {
	for _, c := range []string{"Write-Host 'hi'", `write-output "hi"`, "echo hi"} {
		if !printsOnlyFixedText(c) {
			t.Errorf("%q prints only fixed text", c)
		}
	}
	for _, c := range []string{"Write-Host $env:HOME", "Write-Output (Get-Date)", "Get-Date"} {
		if printsOnlyFixedText(c) {
			t.Errorf("%q does more than print fixed text", c)
		}
	}
}

func TestCRLFEdit(t *testing.T) {
	if !usesCRLF("a\r\nb\r\n") || usesCRLF("a\nb\n") || usesCRLF("a\nb\nc\r\n") {
		t.Error("usesCRLF")
	}
	if withCRLF("a\nb\r\nc") != "a\r\nb\r\nc" || withCRLF("one line") != "one line" {
		t.Error("withCRLF")
	}
}
