package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFrontmatter(t *testing.T) {
	fm, body, ok := Frontmatter("---\nname: pdf\ndescription: \"Read, fill and merge PDFs: forms, tables.\"\nlicense: MIT\nmetadata:\n  version: 2\n---\n# PDF\n\nUse pypdf.\n")
	if !ok || fm["name"] != "pdf" || fm["description"] != "Read, fill and merge PDFs: forms, tables." || fm["license"] != "MIT" || body != "# PDF\n\nUse pypdf.\n" {
		t.Fatalf("%q %q %v", fm, body, ok)
	}
	fm, _, _ = Frontmatter("---\r\nname: x\r\ndescription: >\r\n  Folded over\r\n  two lines.\r\n---\r\nbody")
	if fm["description"] != "Folded over two lines." {
		t.Fatalf("folded %q", fm["description"])
	}
	fm, _, _ = Frontmatter("---\nname: x\ndescription: Goes on\n  over an indented line\nallowed-tools: Read, Grep\n---\n")
	if fm["description"] != "Goes on over an indented line" || fm["allowed-tools"] != "Read, Grep" {
		t.Fatalf("continued %q", fm)
	}
	fm, _, _ = Frontmatter("---\nname: 'it''s'\ndescription: |\n  line one\n  line two\n---\n")
	if fm["name"] != "it's" || fm["description"] != "line one\nline two" {
		t.Fatalf("literal %q", fm)
	}
	if _, _, ok := Frontmatter("# no frontmatter"); ok {
		t.Fatal("found frontmatter in plain markdown")
	}
}

// Skills are found in the project's and the home folder's .caveira and
// .claude skill folders; the project's win, and turning one off keeps it
// out of the prompt and the tool.
func TestDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	proj := t.TempDir()
	os.Mkdir(filepath.Join(proj, ".git"), 0o755)
	work := filepath.Join(proj, "src")
	os.Mkdir(work, 0o755)

	write(t, filepath.Join(home, ".caveira", "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: User's PDF skill.\n---\nuser pdf body")
	write(t, filepath.Join(home, ".claude", "skills", "brand", "SKILL.md"), "---\nname: brand-guide\ndescription: The house style.\n---\nUse the palette in colors.md.")
	write(t, filepath.Join(home, ".claude", "skills", "brand", "colors.md"), "red")
	write(t, filepath.Join(home, ".claude", "skills", "brand", "scripts", "check.py"), "print()")
	write(t, filepath.Join(proj, ".claude", "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: The project's PDF skill.\n---\nproject pdf body")
	write(t, filepath.Join(proj, ".caveira", "skills", "deploy", "SKILL.md"), "---\ndescription: How this project ships.\n---\nmake deploy")
	write(t, filepath.Join(proj, ".caveira", "skills", "nodesc", "SKILL.md"), "---\nname: nodesc\n---\nno description")
	write(t, filepath.Join(proj, ".caveira", "skills", "README.md"), "not a skill folder")

	got := Find(work)
	if len(got) != 3 {
		t.Fatalf("found %+v", got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s := byName["pdf"]; s.Description != "The project's PDF skill." || s.Where != ".claude/skills" || s.Installed {
		t.Fatalf("pdf %+v", s)
	}
	if s := byName["deploy"]; s.Where != ".caveira/skills" {
		t.Fatalf("deploy (named after its folder) %+v", s)
	}
	if s := byName["brand-guide"]; s.Where != "~/.claude/skills" || !s.Enabled {
		t.Fatalf("brand %+v", s)
	}

	sec := Section(Enabled(work))
	if !strings.Contains(sec, "- brand-guide: The house style.") || !strings.Contains(sec, "- pdf: The project's PDF skill.") || strings.Contains(sec, "user pdf body") {
		t.Fatalf("section:\n%s", sec)
	}
	if Section(nil) != "" {
		t.Fatal("no skills should cost no prompt")
	}

	tool := NewTool(work)
	res := tool.Run(context.Background(), json.RawMessage(`{"name":"brand-guide"}`))
	if res.IsError || !strings.Contains(res.Output, "Use the palette in colors.md.") || !strings.Contains(res.Output, "- colors.md") || !strings.Contains(res.Output, filepath.Join("scripts", "check.py")) {
		t.Fatalf("tool: %+v", res)
	}
	if tool.Kind() != 0 {
		t.Fatal("loading a skill should not ask")
	}

	if err := SetEnabled("brand-guide", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Section(Enabled(work)), "brand-guide") {
		t.Fatal("a disabled skill is still offered")
	}
	if res := tool.Run(context.Background(), json.RawMessage(`{"name":"brand-guide"}`)); !res.IsError {
		t.Fatal("the tool loaded a disabled skill")
	}
	SetEnabled("brand-guide", true)

	// Only installed skills can be removed.
	if err := Remove(work, "brand-guide"); err == nil || !strings.Contains(err.Error(), "turn it off instead") {
		t.Fatalf("removed a Claude skill: %v", err)
	}
	write(t, filepath.Join(home, ".caveira", "skills", "mine", "SKILL.md"), "---\nname: mine\ndescription: x\n---\n")
	if err := Remove(work, "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".caveira", "skills", "mine")); err == nil {
		t.Fatal("still there")
	}
}

func TestParseSource(t *testing.T) {
	for in, want := range map[string]Source{
		"https://github.com/anthropics/skills":                               {Owner: "anthropics", Repo: "skills"},
		"https://github.com/anthropics/skills.git":                           {Owner: "anthropics", Repo: "skills"},
		"https://github.com/anthropics/skills/tree/main/skills/pdf":          {Owner: "anthropics", Repo: "skills", Ref: "main", Dir: "skills/pdf"},
		"https://github.com/anthropics/skills/blob/main/skills/pdf/SKILL.md": {Owner: "anthropics", Repo: "skills", Ref: "main", Dir: "skills/pdf"},
		"github.com/anthropics/skills/tree/v1":                               {Owner: "anthropics", Repo: "skills", Ref: "v1"},
		"anthropics/skills":                                                  {Owner: "anthropics", Repo: "skills"},
		"anthropics/skills/skills/pdf":                                       {Owner: "anthropics", Repo: "skills", Dir: "skills/pdf"},
		"anthropics/skills/skills/pdf/SKILL.md":                              {Owner: "anthropics", Repo: "skills", Dir: "skills/pdf"},
	} {
		got, err := ParseSource(in)
		if err != nil || got != want {
			t.Errorf("ParseSource(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "pdf", "https://gitlab.com/a/b", "https://github.com/a/b/issues", "./skills/pdf", "/abs/path"} {
		if _, err := ParseSource(bad); err == nil {
			t.Errorf("ParseSource(%q) took it", bad)
		}
	}
}

// repoTarball is a GitHub tarball: everything under one top folder.
func repoTarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/skills/pdf/evil", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/../../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallFromGitHub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	tgz := repoTarball(t, map[string]string{
		"README.md":                  "repo",
		"skills/pdf/SKILL.md":        "---\nname: pdf\ndescription: PDFs.\n---\nbody",
		"skills/pdf/scripts/fill.py": "print('fill')",
		"skills/docx/SKILL.md":       "---\nname: docx\ndescription: Word files.\n---\nbody",
		"other/notes.md":             "no skill here",
	})
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tarball") {
			http.NotFound(w, r)
			return
		}
		w.Write(tgz)
	}))
	defer srv.Close()
	GitHubAPI = srv.URL
	t.Cleanup(func() { GitHubAPI = "https://api.github.com" })

	// One skill, by its folder.
	got, err := Install(context.Background(), Source{Owner: "owner", Repo: "repo", Ref: "main", Dir: "skills/pdf"})
	if err != nil || len(got) != 1 || got[0].Name != "pdf" {
		t.Fatalf("%+v %v", got, err)
	}
	if paths[0] != "/repos/owner/repo/tarball/main" {
		t.Fatalf("asked for %s", paths[0])
	}
	if b, _ := os.ReadFile(filepath.Join(InstallDir(), "pdf", "scripts", "fill.py")); string(b) != "print('fill')" {
		t.Fatal("bundled file missing")
	}
	if _, err := os.Lstat(filepath.Join(InstallDir(), "pdf", "evil")); err == nil {
		t.Fatal("a link was unpacked")
	}

	// A collection installs every skill in it, and again replaces them.
	got, err = Install(context.Background(), Source{Owner: "owner", Repo: "repo"})
	if err != nil || len(got) != 2 {
		t.Fatalf("collection %+v %v", got, err)
	}
	if s := Find(""); len(s) != 2 || !s[0].Installed {
		t.Fatalf("installed %+v", s)
	}

	if _, err := Install(context.Background(), Source{Owner: "owner", Repo: "repo", Dir: "other"}); err == nil || !strings.Contains(err.Error(), "no SKILL.md") {
		t.Fatalf("folder without skills: %v", err)
	}
	if _, err := Install(context.Background(), Source{Owner: "nobody", Repo: "x"}); err == nil || !strings.Contains(err.Error(), "not there") {
		t.Fatalf("missing repo: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(home, "..", "escape")); len(left) != 0 {
		t.Fatal("an entry escaped the folder")
	}
}
