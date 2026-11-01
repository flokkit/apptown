package agentenv

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Each skill is a SKILL.md under both user-level discovery folders (pi and
// others read ~/.agents/skills, Claude Code ~/.claude/skills), and its front
// matter reads back as the exact name and description, whatever punctuation
// the description has: a description YAML cannot read hides the skill.
func TestSkillFilesAreDiscoverableAndReadBackExactly(t *testing.T) {
	t.Parallel()
	descriptions := []string{
		"Build images: apply runtime rules",
		"# not a comment",
		`Quotes "double" and 'single'`,
		"[images, builds]",
	}
	type skill struct{ Name, Description, Body string }
	var skills []skill
	var files []skillFile
	for i, d := range descriptions {
		s := skill{Name: "s" + string(rune('a'+i)), Description: d, Body: "# How\n\nstep"}
		skills = append(skills, s)
		files = append(files, SkillFiles(s.Name, s.Description, s.Body)...)
	}
	if len(files) != 2*len(skills) {
		t.Fatalf("%d files for %d skills", len(files), len(skills))
	}
	for i, f := range files {
		s := skills[i/2]
		if want := []string{".agents/skills/", ".claude/skills/"}[i%2] + s.Name + "/SKILL.md"; f.Path != want {
			t.Errorf("file %d at %s, want %s", i, f.Path, want)
		}
		text := string(f.Content)
		front, body, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
		if !strings.HasPrefix(text, "---\n") || !ok {
			t.Fatalf("%s has no front matter: %q", f.Path, text)
		}
		var meta map[string]any
		if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
			t.Fatalf("%s front matter: %v", f.Path, err)
		}
		if meta["name"] != s.Name || meta["description"] != s.Description || len(meta) != 2 {
			t.Errorf("%s reads back as %#v", f.Path, meta)
		}
		if strings.TrimSpace(body) != s.Body {
			t.Errorf("%s body: %q", f.Path, body)
		}
	}
}
