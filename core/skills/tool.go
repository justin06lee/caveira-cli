package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/justin06lee/caveira/core/tools"
)

// maxListed is how many skills the system prompt names; past that the
// prompt would cost more than the skills save.
const maxListed = 60

// Section is the system prompt's part about skills: each enabled skill's
// name and description, and how to load one. Empty when there are none,
// so a session without skills pays nothing for them.
func Section(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Skills\n\nSkills are packaged instructions for particular kinds of tasks, each a folder with a SKILL.md. When a task matches a skill's description, call the skill tool with its name before starting, and follow what it loads; paths in it are relative to the skill's folder. Do not load a skill the task does not need.\n\n")
	for i, s := range list {
		if i == maxListed {
			fmt.Fprintf(&sb, "- …and %d more, which the skill tool also loads by name\n", len(list)-maxListed)
			break
		}
		desc := s.Description
		if len(desc) > 300 {
			desc = desc[:300] + "…"
		}
		fmt.Fprintf(&sb, "- %s: %s\n", s.Name, desc)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Use offers the skill tool in reg when there are skills on for workDir,
// and takes it away when there are none, so a session without skills
// pays nothing for them.
func Use(reg *tools.Registry, workDir string) {
	reg.Unregister("skill")
	if len(Enabled(workDir)) > 0 {
		reg.Register(NewTool(workDir))
	}
}

// NewTool is the skill tool for workDir: it loads an enabled skill's
// instructions by name.
func NewTool(workDir string) tools.Tool { return &skillTool{workDir: workDir} }

type skillTool struct{ workDir string }

func (t *skillTool) Name() string     { return "skill" }
func (t *skillTool) Kind() tools.Kind { return tools.KindRead }
func (t *skillTool) Description() string {
	return "Load a skill's instructions by name, from the Skills listed in the system prompt. Call it before doing a task a skill covers, then follow the instructions it returns. It also lists the files bundled with the skill; read them with read_file when the instructions say to."
}
func (t *skillTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "The skill's name, as listed under Skills."},
		},
		"required": []string{"name"},
	}
}
func (t *skillTool) Preview(args json.RawMessage) string {
	var a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Name
}

func (t *skillTool) Run(_ context.Context, args json.RawMessage) tools.Result {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Name) == "" {
		return tools.Result{Output: "Error: give the skill's name, as listed under Skills.", Summary: "no name", IsError: true}
	}
	var names []string
	for _, s := range Enabled(t.workDir) {
		names = append(names, s.Name)
		if !strings.EqualFold(s.Name, strings.TrimSpace(a.Name)) {
			continue
		}
		_, body, err := readSkillFile(s.Path)
		if err != nil {
			return tools.Result{Output: "Error: " + err.Error(), Summary: "unreadable", IsError: true}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "# Skill: %s\nFolder: %s\n\n%s", s.Name, s.Dir, strings.TrimSpace(body))
		if files := bundled(s.Dir); len(files) > 0 {
			sb.WriteString("\n\nFiles in the skill's folder:\n")
			for _, f := range files {
				sb.WriteString("- " + f + "\n")
			}
		}
		out := tools.Bound(sb.String(), tools.OutputLimit)
		return tools.Result{Output: out, Summary: "loaded " + s.Name, Path: s.Path}
	}
	return tools.Result{
		Output:  fmt.Sprintf("Error: no skill called %q. Skills: %s", a.Name, strings.Join(names, ", ")),
		Summary: "no skill " + a.Name, IsError: true,
	}
}

// bundled lists the files in a skill's folder other than SKILL.md, up
// to 100.
func bundled(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= 100 {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != "SKILL.md" && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, rel)
		}
		return nil
	})
	return out
}
