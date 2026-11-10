package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tools take any path the model writes, inside the working directory or
// not (the approval is what guards them, per tool, not a path check), so
// what matters on Windows is that every spelling of a path means what it
// would mean to Windows: the same file, not another one by accident.
func TestResolveWindowsSpellings(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	vol := filepath.VolumeName(dir)
	other := `D:`
	if strings.EqualFold(vol, other) {
		other = `E:`
	}
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		in, abs, rel string
	}{
		{`sub\f.txt`, filepath.Join(dir, "sub", "f.txt"), "sub/f.txt"},
		{`sub/f.txt`, filepath.Join(dir, "sub", "f.txt"), "sub/f.txt"},
		{`.\sub\..\f.txt`, filepath.Join(dir, "f.txt"), "f.txt"},
		{`..\out.txt`, filepath.Join(root, "out.txt"), filepath.ToSlash(filepath.Join(root, "out.txt"))},
		{`../out.txt`, filepath.Join(root, "out.txt"), filepath.ToSlash(filepath.Join(root, "out.txt"))},
		{`sub\..\..\out.txt`, filepath.Join(root, "out.txt"), filepath.ToSlash(filepath.Join(root, "out.txt"))},
		{filepath.Join(dir, "a.txt"), filepath.Join(dir, "a.txt"), "a.txt"},
		{filepath.ToSlash(filepath.Join(dir, "a.txt")), filepath.Join(dir, "a.txt"), "a.txt"},
		{strings.ToUpper(filepath.Join(dir, "a.txt")), strings.ToUpper(filepath.Join(dir, "a.txt")), "A.TXT"}, // case is not a difference on Windows
		{`\Windows\win.ini`, vol + `\Windows\win.ini`, filepath.ToSlash(vol + `\Windows\win.ini`)},
		{vol + `rel.txt`, filepath.Join(dir, "rel.txt"), "rel.txt"},
		{other + `rel.txt`, other + `\rel.txt`, other + `/rel.txt`},
		{`\\server\share\x.txt`, `\\server\share\x.txt`, `//server/share/x.txt`},
		{`~\n.txt`, filepath.Join(home, "n.txt"), filepath.ToSlash(filepath.Join(home, "n.txt"))},
		{`~/n.txt`, filepath.Join(home, "n.txt"), filepath.ToSlash(filepath.Join(home, "n.txt"))},
	} {
		abs, rel := resolve(dir, c.in)
		if !strings.EqualFold(abs, c.abs) || !strings.EqualFold(rel, c.rel) {
			t.Errorf("%q: abs %q rel %q, want abs %q rel %q", c.in, abs, rel, c.abs, c.rel)
		}
	}
}

func TestToolsOnWindowsPaths(t *testing.T) {
	dir := t.TempDir()
	reg := Default(dir)
	ctx := context.Background()
	call := func(name string, a map[string]any) Result {
		b, _ := json.Marshal(a)
		return reg.Run(ctx, name, b)
	}

	// Written with backslashes, read back with slashes: one file.
	if r := call("write_file", map[string]any{"path": `src\deep\a.go`, "content": "package a\r\nfunc Hello() {}\r\n"}); r.IsError {
		t.Fatal(r.Output)
	}
	if r := call("read_file", map[string]any{"path": "src/deep/a.go"}); r.IsError || !strings.Contains(r.Output, "1\tpackage a\n") {
		t.Fatalf("read: %q", r.Output)
	}

	// A model writes \n; a file with CRLF is edited all the same, and keeps them.
	r := call("edit_file", map[string]any{"path": `src\deep\a.go`, "old_string": "package a\nfunc Hello() {}", "new_string": "package a\n\nfunc Hello() int { return 1 }"})
	if r.IsError {
		t.Fatalf("edit: %q", r.Output)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "src", "deep", "a.go"))
	if string(b) != "package a\r\n\r\nfunc Hello() int { return 1 }\r\n" {
		t.Errorf("file is %q", b)
	}

	// Patterns written with backslashes are patterns, and results come back with slashes.
	r = call("glob", map[string]any{"pattern": `src\**\*.go`})
	if r.IsError || strings.TrimSpace(r.Output) != "src/deep/a.go" {
		t.Errorf("glob: %q", r.Output)
	}
	r = call("grep", map[string]any{"pattern": "Hello", "glob": `src\deep\*.go`, "output_mode": "content"})
	if r.IsError || !strings.Contains(r.Output, "src/deep/a.go") {
		t.Errorf("grep with a backslash glob: %q", r.Output)
	}
	r = call("grep", map[string]any{"pattern": "Hello", "glob": "**/*.go", "path": "src"})
	if r.IsError || !strings.Contains(r.Output, "src/deep/a.go") {
		t.Errorf("grep **: %q", r.Output)
	}
	r = call("list_dir", map[string]any{"path": `src\deep`})
	if r.IsError || !strings.Contains(r.Output, "a.go") {
		t.Errorf("list: %q", r.Output)
	}

	// Outside the project is allowed on every system, by absolute path or
	// by climbing out; it lands where Windows says it does.
	outside := filepath.Join(filepath.Dir(dir), "outside-"+filepath.Base(dir)+".txt")
	defer os.Remove(outside)
	if r := call("write_file", map[string]any{"path": `..\` + filepath.Base(outside), "content": "x"}); r.IsError {
		t.Fatal(r.Output)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("..\\ did not reach the parent: %v", err)
	}
	// A drive-relative name is not a file called "C:x" in the project (NTFS would make that a data stream).
	if r := call("write_file", map[string]any{"path": filepath.VolumeName(dir) + "ads.txt", "content": "x"}); r.IsError {
		t.Fatal(r.Output)
	}
	if _, err := os.Stat(filepath.Join(dir, "ads.txt")); err != nil {
		t.Errorf("C:ads.txt is not in the project: %v", err)
	}
}
