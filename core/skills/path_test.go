package skills

import "testing"

// A skill is unpacked from a tarball anyone can publish, so a name in it
// must not be able to climb out of the folder it goes in, on any system.
// Windows takes more than a slash and two dots for a path: a backslash
// separates folders, a drive letter makes a name absolute, a colon makes a
// data stream, and some names are devices.
func TestLocalName(t *testing.T) {
	for _, ok := range []string{"SKILL.md", "scripts/run.sh", "a/b/c.txt", ".hidden", "a..b", "COM", "CONSOLE.txt", "com10"} {
		if !localName(ok) {
			t.Errorf("%q is a fine name", ok)
		}
	}
	for _, bad := range []string{
		"..", "../x", "a/../../x", `..\x`, `a\..\..\x`, `a\b`, `C:x`, `C:\x`, `C:/x`, "c:", "a/b:stream", "x:",
		"NUL", "nul.txt", "a/CON", "con.md", "AUX.", "COM1", "lpt9.txt", "a/PRN ", "x\x00y",
	} {
		if localName(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}
