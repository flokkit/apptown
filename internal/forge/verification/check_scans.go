package verification

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/strs"
)

func scanFindingsSummary(scanner string, fs []conformance.Finding) string {
	return fmt.Sprintf("%d finding(s) — %s", len(fs), findingsList(fs))
}

// findingsList names the first few findings by severity, title and
// identifier, then how many more.
func findingsList(fs []conformance.Finding) string {
	var parts []string
	for i, f := range fs {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(fs)-3))
			break
		}
		t := f.Title
		if len(f.Identifiers) > 0 {
			t += " (" + f.Identifiers[0] + ")"
		}
		parts = append(parts, f.Severity+": "+t)
	}
	return strs.Truncate(strings.Join(parts, "; "), 260)
}
