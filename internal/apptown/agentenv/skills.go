package agentenv

import "encoding/json"

// SkillFile is a skill's SKILL.md at Path, relative to the agent's HOME.
type SkillFile struct {
	Path    string
	Content []byte
}

// skillRoots are the user-level folders, under HOME, where coding agents
// discover skills: ~/.agents/skills (the Agent Skills user folder, which
// OpenAI's coding agent reads, and one of pi's global skill folders, read
// without trusting the project) and ~/.claude/skills (Claude Code's
// personal skills). AppTown writes a
// project's skills into the HOME it generates for the agent, never into
// the repository's own .agents/skills or .claude/skills: those belong to
// the change's tree, and a file AppTown put there would be part of the
// agent's work.
var skillRoots = []string{".agents/skills", ".claude/skills"}

// SkillFiles renders one skill as a SKILL.md in the Agent Skills format
// (front matter with name and description, then the how-to), once under
// each discovery folder.
func SkillFiles(name, description, body string) []SkillFile {
	// JSON strings are YAML double-quoted scalars: any description reads
	// back as written.
	quotedName, _ := json.Marshal(name)
	quotedDescription, _ := json.Marshal(description)
	content := []byte("---\nname: " + string(quotedName) + "\ndescription: " + string(quotedDescription) + "\n---\n\n" + body + "\n")
	out := make([]SkillFile, 0, len(skillRoots))
	for _, root := range skillRoots {
		out = append(out, SkillFile{Path: root + "/" + name + "/SKILL.md", Content: content})
	}
	return out
}
