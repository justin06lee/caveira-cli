// Package skills finds Agent Skills and offers them to the model. A skill
// is a folder with a SKILL.md: YAML frontmatter with a name and a
// description, then instructions, and any files the instructions use.
// Only each skill's name and description go in the system prompt; the
// model loads a skill's SKILL.md with the skill tool when a task calls
// for it, and reads its other files as it needs them.
//
// Skills are looked for in the project's .caveira/skills and
// .claude/skills, then ~/.caveira/skills and ~/.claude/skills, so skills
// set up for Claude Code work as they are. The first skill of a name
// wins. Skills can be turned off without removing them
// (~/.caveira/skills.json), and installed from GitHub into
// ~/.caveira/skills.
package skills

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/justin06lee/caveira/core/config"
)

// Skill is one skill found on disk.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Dir is the skill's folder and Path its SKILL.md.
	Dir  string `json:"dir"`
	Path string `json:"path"`
	// Where says which folder it was found in, as shown: ~/.caveira/skills,
	// .claude/skills…
	Where string `json:"where"`
	// Installed says it is in ~/.caveira/skills, where caveira can
	// remove it.
	Installed bool `json:"installed"`
	Enabled   bool `json:"enabled"`
}

// InstallDir is ~/.caveira/skills, where installed skills go.
func InstallDir() string { return filepath.Join(config.Dir(), "skills") }

func statePath() string { return filepath.Join(config.Dir(), "skills.json") }

type state struct {
	Disabled []string `json:"disabled,omitempty"`
}

func readState() state {
	var s state
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SetEnabled turns the skill name on or off for every project.
func SetEnabled(name string, on bool) error {
	s := readState()
	s.Disabled = slices.DeleteFunc(s.Disabled, func(n string) bool { return n == name })
	if !on {
		s.Disabled = append(s.Disabled, name)
		slices.Sort(s.Disabled)
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(statePath(), append(b, '\n'), 0o600)
}

// root is the project's folder: the git root above workDir, or workDir.
func root(workDir string) string {
	for d := workDir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return workDir
		}
	}
}

// searchPath is where skills are looked for, first wins, with how each
// folder is shown.
func searchPath(workDir string) [][2]string {
	home, _ := os.UserHomeDir()
	var out [][2]string
	if workDir != "" {
		r := root(workDir)
		out = append(out,
			[2]string{filepath.Join(r, ".caveira", "skills"), ".caveira/skills"},
			[2]string{filepath.Join(r, ".claude", "skills"), ".claude/skills"})
	}
	return append(out,
		[2]string{InstallDir(), "~/.caveira/skills"},
		[2]string{filepath.Join(home, ".claude", "skills"), "~/.claude/skills"})
}

// Find is every skill for workDir, enabled or not, by name.
func Find(workDir string) []Skill {
	disabled := readState().Disabled
	seen := map[string]bool{}
	var out []Skill
	for _, sp := range searchPath(workDir) {
		entries, err := os.ReadDir(sp[0])
		if err != nil {
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(sp[0], e.Name())
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			sk, err := Read(dir)
			if err != nil || seen[sk.Name] {
				continue
			}
			seen[sk.Name] = true
			sk.Where = sp[1]
			sk.Installed = sp[0] == InstallDir()
			sk.Enabled = !slices.Contains(disabled, sk.Name)
			out = append(out, sk)
		}
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Enabled is the skills on for workDir.
func Enabled(workDir string) []Skill {
	return slices.DeleteFunc(Find(workDir), func(s Skill) bool { return !s.Enabled })
}

// Read reads the skill in dir. A SKILL.md without a name is named after
// its folder; one without a description is no skill, since the model
// could not tell when to use it.
func Read(dir string) (Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	fm, _, err := readSkillFile(path)
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(fm["name"])
	if name == "" {
		name = filepath.Base(dir)
	}
	desc := strings.Join(strings.Fields(fm["description"]), " ")
	if desc == "" {
		return Skill{}, fmt.Errorf("%s has no description", path)
	}
	return Skill{Name: name, Description: desc, Dir: dir, Path: path}, nil
}

func readSkillFile(path string) (map[string]string, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	fm, body, ok := Frontmatter(string(b))
	if !ok {
		return nil, "", fmt.Errorf("%s has no frontmatter", path)
	}
	return fm, body, nil
}

// Frontmatter splits a SKILL.md into its frontmatter's top-level keys
// and the body after it. It reads the YAML skills use: key: value,
// quoted values, and block values (| and >) or values that go on over
// indented lines. Nested keys are not read.
func Frontmatter(text string) (map[string]string, string, bool) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, text, false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, text, false
	}
	head := rest[:end]
	body := strings.TrimLeft(rest[end+4:], "-")
	body = strings.TrimPrefix(body, "\n")

	out := map[string]string{}
	var key string
	var block []string
	flush := func() {
		if key == "" {
			return
		}
		switch strings.TrimRight(out[key], "-+") {
		case "|":
			out[key] = strings.Join(block, "\n")
		case ">":
			out[key] = strings.Join(block, " ")
		default:
			if len(block) > 0 {
				out[key] = strings.TrimSpace(out[key] + " " + strings.Join(block, " "))
			}
		}
		key, block = "", nil
	}
	sc := bufio.NewScanner(strings.NewReader(head))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if key != "" {
				block = append(block, strings.TrimSpace(line))
			}
			continue
		}
		flush()
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			if v[0] == '"' {
				v = strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
			} else {
				v = strings.ReplaceAll(v[1:len(v)-1], `''`, `'`)
			}
		}
		out[key] = v
	}
	flush()
	return out, body, true
}

// Remove deletes the installed skill name from ~/.caveira/skills. Skills
// found elsewhere are not caveira's to delete; they can be turned off.
func Remove(workDir, name string) error {
	for _, s := range Find(workDir) {
		if s.Name != name {
			continue
		}
		if !s.Installed {
			return fmt.Errorf("%s is in %s, which caveira did not install; turn it off instead", name, s.Where)
		}
		if err := os.RemoveAll(s.Dir); err != nil {
			return err
		}
		return SetEnabled(name, true)
	}
	return errors.New("no skill called " + name)
}
