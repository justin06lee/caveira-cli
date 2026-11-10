package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/proc"
)

const (
	bashDefaultTimeout = 2 * time.Minute
	bashMaxTimeout     = 10 * time.Minute
	bashOutputLimit    = 30_000
)

type bashTool struct{ dir string }

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Kind() Kind   { return KindExecute }
func (t *bashTool) Description() string {
	sh := DetectShell()
	return "Run a shell command in the working directory and return its combined stdout and stderr plus the exit code. " +
		shellNote(sh) +
		"Use it for commands that do something in the project: builds, tests, git, package managers, and anything else with a CLI. " +
		"Never use it to talk to the user or to print text you could write in your reply: echo and printf of fixed text are refused. " +
		"The shell is non-interactive: " +
		"commands that prompt for input will hang until the timeout, so pass flags like -y or --no-edit. " +
		"Prefer the dedicated read_file, edit_file, glob, and grep tools over cat, sed, find, and grep. " +
		"Do not use cd to move around; give absolute paths or paths relative to the working directory instead. " +
		"Long output is truncated in the middle; " + trimAdvice(sh) + " when you only need part of it."
}

// shellNote tells the model which shell it really has. The tool is called
// bash everywhere, since sessions and the interface know it by that name.
func shellNote(sh Shell) string {
	switch sh.Kind {
	case "powershell":
		return "The shell is " + sh.Name + ", not bash, whatever this tool is called: write PowerShell (Get-ChildItem, Select-String, $env:NAME, ; to chain, no && before PowerShell 7), not bash. "
	case "cmd":
		return "The shell is cmd.exe, not bash, whatever this tool is called: write cmd syntax. "
	}
	if runtime.GOOS == "windows" {
		return "The shell is " + sh.Name + " on Windows: write bash, and give paths with forward slashes (C:/Users/me/project). "
	}
	return ""
}

func trimAdvice(sh Shell) string {
	if sh.Kind == "powershell" {
		return "pipe through Select-Object -First or -Last, or Select-String"
	}
	return "pipe through head, tail, or grep"
}
func (t *bashTool) Schema() map[string]any {
	return schema(map[string]any{
		"command":     prop("string", "The command to run, as you would type it in the shell the tool description names (bash unless it says otherwise)."),
		"description": prop("string", "A short description of what the command does, in plain words, shown to the user."),
		"timeout":     prop("integer", "Timeout in seconds. Defaults to 120, maximum 600."),
	}, "command")
}
func (t *bashTool) Preview(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = decode(args, &a)
	return strings.TrimSpace(a.Command)
}

func (t *bashTool) Run(ctx context.Context, args json.RawMessage) Result {
	var a struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Timeout     int    `json:"timeout"`
	}
	if err := decode(args, &a); err != nil {
		return errorResult("bad arguments: %v", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return errorResult("command is required")
	}
	if printsOnlyFixedText(a.Command) {
		r := errorResult("not run: this only prints fixed text, which reaches no one. Say it in your reply instead, as plain text.")
		r.Summary = "not run · only prints fixed text"
		return r
	}
	timeout := bashDefaultTimeout
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout) * time.Second
		// Some models send milliseconds out of habit.
		if a.Timeout > 600 && a.Timeout%1000 == 0 {
			timeout = time.Duration(a.Timeout) * time.Millisecond
		}
	}
	if timeout > bashMaxTimeout {
		timeout = bashMaxTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sh := DetectShell()
	cmd := exec.CommandContext(ctx, sh.Path, sh.Args(a.Command)...)
	cmd.Dir = t.dir
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(),
		"CAVEIRA=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"TERM=dumb",
		"NO_COLOR=1",
	)
	// Own process group (a job object on Windows), so killing on timeout
	// takes the children with it.
	proc.Setup(cmd)
	cmd.WaitDelay = 2 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Start()
	if err == nil {
		proc.Adopt(cmd)
		err = cmd.Wait()
		proc.Release(cmd)
	}
	elapsed := time.Since(start).Round(10 * time.Millisecond)

	exit := 0
	var status string
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		exit = -1
		status = fmt.Sprintf("Command timed out after %s and was killed.", timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		exit = -1
		status = "Command was cancelled."
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
			if exit == -1 && ee.ProcessState != nil {
				if sig, ok := proc.Signal(ee); ok {
					status = fmt.Sprintf("Command was killed by signal %s.", sig)
				}
			}
		} else {
			return errorResult("could not start command: %v", err)
		}
	}

	// Windows programs and PowerShell end their lines with \r\n; the model
	// and the screen want \n (a bare \r sent to a terminal returns its cursor
	// and the line was found to vanish from the interface).
	text := strings.TrimRight(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n")
	text, truncated := truncateOutput(text, bashOutputLimit)

	var sb strings.Builder
	if text != "" {
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	if status != "" {
		sb.WriteString(status)
		sb.WriteString("\n")
	}
	if exit != 0 {
		fmt.Fprintf(&sb, "[exit code %d]", exit)
	} else if text == "" {
		sb.WriteString("[no output, exit code 0]")
	}
	if truncated {
		sb.WriteString("\n[output was truncated; rerun with head/tail/grep to see a specific part]")
	}

	lines := 0
	if text != "" {
		lines = strings.Count(text, "\n") + 1
	}
	summary := fmt.Sprintf("exit %d · %s · %s", exit, plural(lines, "line"), elapsed)
	if status != "" {
		summary = strings.TrimSuffix(status, ".")
	}
	return Result{Output: sb.String(), Summary: summary, IsError: exit != 0}
}

// printsOnlyFixedText spots a command that is nothing but echo or printf
// of literal text: no variables, substitutions, redirects, pipes, or other
// commands. Its output is known before it runs and goes only back to the
// model, so running it does nothing; models that call it are trying to
// talk through the shell.
func printsOnlyFixedText(command string) bool {
	c := strings.TrimSpace(command)
	name, _, _ := strings.Cut(c, " ")
	if !printsText(name) {
		return false
	}
	return !strings.ContainsAny(c, "$`<>|;&()\n*?[~{")
}
