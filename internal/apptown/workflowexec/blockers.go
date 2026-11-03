package workflowexec

import (
	"strings"
	"unicode"
)

// reportBlockers reads the declared Blockers: field of a change report.
// Following bullets up to the next non-list line name person actions.
// A bullet declaring no blockers is not a person action.
func reportBlockers(report string) []string {
	var actions []string
	in := false
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		if !in {
			if line == "Blockers:" {
				in = true
			}
			continue
		}
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			break
		}
		if action := strings.TrimSpace(strings.TrimPrefix(line, "- ")); action != "" {
			switch strings.ToLower(strings.TrimRightFunc(action, unicode.IsPunct)) {
			case "none", "n/a", "no blockers":
				continue
			}
			actions = append(actions, action)
		}
	}
	return actions
}
