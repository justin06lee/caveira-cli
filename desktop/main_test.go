package main

import (
	"os"
	"testing"

	"github.com/justin06lee/caveira/core/mcp"
)

func TestMain(m *testing.M) {
	// Every test has a home of its own; no built-in server is set up in
	// each.
	mcp.Defaults = nil
	os.Exit(m.Run())
}
