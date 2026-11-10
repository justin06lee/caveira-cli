package ui

import (
	"context"
	"strings"
	"testing"
)

// On Windows /update says why it will not update, for a release build as
// much as for a development one, before it asks anything of the network.
func TestUpdateOnWindowsSaysItDoesNotUpdate(t *testing.T) {
	for _, v := range []string{"v0.1.0", "dev"} {
		_, err := SelfUpdate(context.Background(), v, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "does not update itself on Windows") {
			t.Errorf("%s: %v", v, err)
		}
	}
}
