package conformance

import "flokkit.io/forge/internal/check"

// Finding adds baseline and disposition observations to canonical scan evidence.
type Finding struct {
	check.Finding
	// Regression: how a conformance finding reads against the change's
	// baseline — introduced | preexisting | undecided (§4.2). Empty when no
	// baseline applied.
	Regression string `json:"regression,omitempty"`
	// PreExisting: the finding was already on the change's base commit —
	// carried debt, reported but never gating an ordinary change
	// (design/subsystems/conformance.md §7.3; scan_provenance.go).
	PreExisting bool `json:"preExisting,omitempty"`
	// Filled by the view when a disposition matches:
	Disposition *Disposition `json:"disposition,omitempty"`
}

// ReadReport reads a scanner's report in its declared format: a report that
// cannot be parsed is an error, never an empty one.
func ReadReport(format string, report []byte, source, kind string) ([]Finding, error) {
	fs, err := check.Normalize(format, report, source, kind)
	return WrapFindings(fs), err
}

func WrapFindings(fs []check.Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		out = append(out, Finding{Finding: f})
	}
	return out
}

// RawFindings takes forge's overlay back off: the findings as the library
// itself reported them, for a library function that judges or words them.
func RawFindings(fs []Finding) []check.Finding {
	out := make([]check.Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Finding)
	}
	return out
}

// --- dispositions (layer 3) -------------------------------------------------

// AnnotateFindings fills Disposition on matched findings and returns the actionable
// subset (unmatched findings).
func AnnotateFindings(findings []Finding, ds []Disposition) (actionable []Finding) {
	for i := range findings {
		findings[i].Disposition = nil
		subject := FindingSubject{Fingerprint: findings[i].Fingerprint, Identifiers: findings[i].Identifiers}
		if findings[i].Package != nil {
			subject.Package = findings[i].Package.Name
		}
		if d := MatchDisposition(subject, ds); d != nil {
			findings[i].Disposition = d
		} else {
			actionable = append(actionable, findings[i])
		}
	}
	return actionable
}
