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
// identifier, and the file the scanner found each in, then how many more.
// The file is what makes an image finding actionable: a CVE in a binary
// the base image carries reads like a dependency the app never had until
// its path is shown (complaint #95, /usr/bin/pebble in a Java image).
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
		if f.Location != nil && f.Location.File != "" {
			t += " in " + f.Location.File
		}
		parts = append(parts, f.Severity+": "+t)
	}
	return strs.Truncate(strings.Join(parts, "; "), 260)
}
