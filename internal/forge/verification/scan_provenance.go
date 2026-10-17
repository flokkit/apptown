package verification

// Finding provenance at the gate (design/subsystems/conformance.md §7.3): a landing must
// not be blocked by debt the change did not create. A source scan at the
// gate therefore scans the change's BASE commit with the same binding and
// sorts each finding into INTRODUCED (new in this change — gates) or
// CARRIED (already on the base — reported as owed debt, never a bypass and
// never hidden). Otherwise semgrep findings that sat on main for weeks would
// block an unrelated CSS-and-JS change.
//
// Matching prefers the scanner's own stable fingerprint (semgrep's are
// content hashes, so a moved line still matches) and falls back to
// kind + rule + file, so a line shift alone never turns debt into "new".

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/strs"
)

// scanBase is what the base commit's scan found — or why it could not be
// compared (then every finding gates, as before, and the note says so).
type scanBase struct {
	OK       bool
	Note     string
	Detail   string // why no base image was read, when the note does not say
	SHA      string
	Findings []check.Finding // what the base scan reported, for the library to match against
}

// splitByProvenance sorts findings against the base: carried ones are marked
// PreExisting and returned separately; introduced ones gate. An unavailable
// base (OK=false) returns everything as introduced. The rule is the library's
// (check.Classify); a scanner's finding names the problem itself, so the base
// reporting it is proof the problem was already there.
func splitByProvenance(findings []conformance.Finding, base scanBase) (introduced, carried []conformance.Finding) {
	head := make([]check.Evaluation, 0, len(findings))
	for _, f := range findings {
		head = append(head, check.Evaluation{Findings: []check.Finding{f.Finding}})
	}
	for i, judged := range check.Classify(head, []check.Evaluation{{Findings: base.Findings}}, base.OK) {
		f := findings[i]
		if judged.Provenance == check.PreExisting {
			f.PreExisting = true
			carried = append(carried, f)
			continue
		}
		introduced = append(introduced, f)
	}
	return introduced, carried
}

// scanGate sorts a scan step's findings for its gate. Every finding is
// matched against the base before exceptions are applied: one with an
// exception still accounts for its own occurrence on main, so it cannot
// leave that occurrence free to cover a new finding of its rule in its file.
// Then only findings without an exception gate or count as carried debt.
// Exceptions are annotated on findings in place. Without a comparison
// (compared false) every actionable finding gates.
func scanGate(findings []conformance.Finding, ds []conformance.Disposition, base scanBase, compared bool) (actionable, gating, carried []conformance.Finding) {
	actionable = conformance.AnnotateFindings(findings, ds)
	if !compared {
		return actionable, actionable, nil
	}
	introduced, preexisting := splitByProvenance(findings, base)
	open := func(fs []conformance.Finding) []conformance.Finding {
		var out []conformance.Finding
		for _, f := range fs {
			if f.Disposition == nil {
				out = append(out, f)
			}
		}
		return out
	}
	return actionable, open(introduced), open(preexisting)
}

// carriedSummary names carried debt so it is visible on the step, not hidden.
func carriedSummary(carried []conformance.Finding) string {
	if len(carried) == 0 {
		return ""
	}
	var parts []string
	for i, f := range carried {
		if i >= 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(carried)-4))
			break
		}
		t := f.Title
		if f.Location != nil && f.Location.File != "" {
			t += " @ " + f.Location.File
		}
		parts = append(parts, f.Severity+": "+t)
	}
	return strs.Truncate(strings.Join(parts, "; "), 400)
}

// scanFailReason is a failing scan's reason: how many findings fail and
// against what, then the findings themselves, so the reason alone says
// what to fix.
func scanFailReason(failing []conformance.Finding, base scanBase) string {
	const remedy = " — fix, or suppress each with a justification; blanket override is the wrong tool here"
	listed := ": " + findingsList(failing)
	if base.OK {
		return fmt.Sprintf("%d finding(s) introduced by this change%s%s", len(failing), remedy, listed)
	}
	return fmt.Sprintf("%d actionable finding(s) (%s)%s%s", len(failing), strs.FirstNonBlank(base.Note, "no base to compare with"), remedy, listed)
}
