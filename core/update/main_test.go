package update

import (
	"fmt"
	"os"
	"testing"
)

// realHome is the home the tests started with. codesign finds the
// keychain, and the Developer ID in it, through HOME.
var realHome = os.Getenv("HOME")

// TestMain gives the package's tests a home of their own: launch state and
// the update cache are written under it, and must not land in the real
// ~/.caveira of whoever runs the tests.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "caveira-update-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", os.Getenv("HOME"))
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
