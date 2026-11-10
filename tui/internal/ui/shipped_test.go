//go:build !devserver

package ui

import (
	"strings"
	"testing"
)

// A shipped build has no local model server, so /usage signed out says so,
// not that a local model costs nothing.
func TestShippedUsageSignedOutSaysSignedOut(t *testing.T) {
	m := testModel(t, 100, 30)
	var cost string
	for _, r := range m.costPanel(nil).rows {
		if strings.Contains(strings.ToLower(r[1]), "local") {
			t.Fatalf("/usage mentions a local model in a shipped build: %q", r)
		}
		if r[0] == "cost" {
			cost = r[1]
		}
	}
	if cost != "signed out · /login" {
		t.Fatalf("cost row %q", cost)
	}
}
