package wow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
)

// A guidance block may carry a skill: a how-to an agent loads only when it
// does that one thing. The block's body holds both parts, split by one
// marker line, so the item stays one versioned body with one pin, one hash
// and its checks:
//
//	<the rules every agent reads>
//	<!-- skill: <description: what the how-to covers and when to load it> -->
//	<the how-to>
//
// Delivery (where an agent finds the skill) is the agent environment's job;
// this is only the authored format.

// Skill is the how-to part of a guidance block.
type Skill struct {
	// Name is the block's name with "/" as "-": lowercase letters, digits
	// and hyphens, as agent skill names are.
	Name        string
	Description string
	Body        string
}

// skillMarker is a whole line; the description is one line of text. Any
// line that opens as a skill marker is one, so a malformed marker is refused
// rather than read as an ordinary comment.
var skillMarker = regexp.MustCompile(`(?m)^<!--[ \t]*skill:(.*?)(?:-->)?[ \t]*$`)

// skillName is the Agent Skills naming rule: 1-64 lowercase letters,
// digits and single hyphens, no hyphen at either end.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxSkillDescription = 1024

// errSkill is a body whose skill marker is malformed.
var errSkill = fault.New(fault.Invalid, "a guidance body carries at most one skill, with a description and a how-to")

// SplitSkill returns a block's always-read rules and its skill, or the body
// unchanged and no skill when it has no marker. A body with more than one
// marker, an empty or over-long description, an empty how-to, or a name
// that cannot be a skill's is refused.
func SplitSkill(blockName, body string) (rules string, skill *Skill, err error) {
	marks := skillMarker.FindAllStringSubmatchIndex(body, -1)
	switch len(marks) {
	case 0:
		return body, nil, nil
	case 1:
	default:
		return "", nil, fmt.Errorf("%w: guidance %s has %d skill markers; a block carries at most one skill", errSkill, blockName, len(marks))
	}
	m := marks[0]
	if !strings.HasSuffix(strings.TrimRight(body[m[0]:m[1]], " \t"), "-->") {
		return "", nil, fmt.Errorf("%w: guidance %s: the skill marker is one line ending in -->", errSkill, blockName)
	}
	description := strings.TrimSpace(body[m[2]:m[3]])
	how := strings.TrimSpace(body[m[1]:])
	name := strings.ReplaceAll(blockName, "/", "-")
	switch {
	case description == "":
		return "", nil, fmt.Errorf("%w: guidance %s: the skill marker needs a description saying what the how-to covers and when to load it", errSkill, blockName)
	case len(description) > maxSkillDescription:
		return "", nil, fmt.Errorf("%w: guidance %s: the skill's description is %d characters; the limit is %d", errSkill, blockName, len(description), maxSkillDescription)
	case how == "":
		return "", nil, fmt.Errorf("%w: guidance %s: the skill marker is followed by nothing; the how-to goes below it", errSkill, blockName)
	case len(name) > 64 || !skillName.MatchString(name):
		return "", nil, fmt.Errorf("%w: guidance %s cannot carry a skill: a skill's name (%q) is lowercase letters, digits and single hyphens, at most 64", errSkill, blockName, name)
	}
	return strings.TrimRight(body[:m[0]], " \t\n"), &Skill{Name: name, Description: description, Body: how}, nil
}

// SkillPointer is the line the always-read rules carry for a block's
// skill, so the rules name the how-to wherever the agent lists skills.
func SkillPointer(s Skill) string {
	return "Skill `" + s.Name + "` has the how-to: " + s.Description
}
