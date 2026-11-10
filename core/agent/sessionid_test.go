package agent

import "testing"

func TestValidSessionID(t *testing.T) {
	for _, ok := range []string{"20260101-abc123", "claude-5f2a", "a.b"} {
		if !validSessionID(ok) {
			t.Errorf("%q is a session id", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", `..\x`, "a/b", `a\b`, "C:x", "x:stream", "a\x00"} {
		if validSessionID(bad) {
			t.Errorf("%q is not a session id", bad)
		}
		if _, err := LoadSession(bad); err == nil {
			t.Errorf("LoadSession(%q) read something", bad)
		}
		if HasSession(bad) {
			t.Errorf("HasSession(%q)", bad)
		}
	}
}
