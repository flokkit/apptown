package workflowexec

import "strings"

// reportBlockers reads the declared Blockers: field of a change report.
// Every following bullet up to the next non-list line is a person action.
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
			actions = append(actions, action)
		}
	}
	return actions
}
