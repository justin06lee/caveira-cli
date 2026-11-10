//go:build !devserver

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A shipped build has no flag, variable or setting for a model server or
// key of one's own, and its help names none.
func TestShippedHasNoServerOfYourOwn(t *testing.T) {
	// The child: caveira with the flag, which exits on it.
	if flag := os.Getenv("CAVEIRA_TEST_FLAG"); flag != "" {
		os.Args = []string{"caveira", "-C", os.TempDir(), flag, "http://localhost:11434/v1", "-p", "hi"}
		_ = run()
		os.Exit(0)
	}
	for _, flag := range []string{"--base-url", "--api-key"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestShippedHasNoServerOfYourOwn$")
		cmd.Env = append(os.Environ(), "CAVEIRA_TEST_FLAG="+flag)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "flag provided but not defined: -"+strings.TrimLeft(flag, "-")) {
			t.Errorf("%s: exit %v, output %.200q; want it refused as an unknown flag", flag, err, out)
		}
	}
	help := helpText()
	for _, s := range []string{"base-url", "api-key", "CAVEIRA_BASE_URL", "CAVEIRA_API_KEY", "CAVEIRA_CLOUD_URL", "base_url", "api_key", "local model", "Ollama"} {
		if strings.Contains(help, s) {
			t.Errorf("help mentions %q", s)
		}
	}
}
