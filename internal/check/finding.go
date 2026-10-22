package check

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/strs"
)

// Structured findings (design/subsystems/findings.md): the three-layer model.
// Layer 1 is the native scan-report artifact (canonical evidence, untouched).
// Layer 2 is THIS FILE: a normalized view computed on read — per-FORMAT
// normalizers (OSV, SARIF), never per-tool. Layer 3 is dispositions:
// project-scoped, VEX-shaped acceptance state with justification and expiry.
// Layer 3 is forge's: a disposition is a person's decision, and the library
// never sees one. The scan verdict — actionable = findings − active
// dispositions — is likewise the caller's.

type PackageRef struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
	FixedIn   string `json:"fixedIn,omitempty"`
}

type Location struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
}

type Finding struct {
	Fingerprint string      `json:"fingerprint"`
	Kind        string      `json:"kind"`     // vuln | secret | sast | license
	Severity    string      `json:"severity"` // critical|high|medium|low|unknown
	Identifiers []string    `json:"identifiers,omitempty"`
	Package     *PackageRef `json:"package,omitempty"`
	Location    *Location   `json:"location,omitempty"`
	Title       string      `json:"title"`
	Detail      string      `json:"detail,omitempty"`
	URL         string      `json:"url,omitempty"`
	Source      string      `json:"source"` // binding name
	// Reached: osv-scanner call analysis — does the program actually call the
	// vulnerable code? nil = no data. VEX-grade context: an unreached vuln is
	// the textbook not_affected justification. Never changes the verdict by
	// itself; dispositions stay a human act.
	Reached *bool `json:"reached,omitempty"`
	// Items: the occurrences a conformance check reported (one per line when
	// the check declares occurrences: lines), so a gate can subtract what
	// already fails on the base tree and count only what is new.
	Items []string `json:"items,omitempty"`
}

// sarifSeverity resolves a SARIF finding's severity into our buckets. A
// numeric security-severity (CVSS 0-10, GitHub's convention) wins when
// present; else the effective level is the result's own level, else the
// rule's defaultConfiguration.level (SARIF 2.1.0 §3.27.10 — where semgrep
// puts it). Returns "" when no severity signal exists anywhere, leaving the
// default to the caller.
func sarifSeverity(resultLevel, ruleLevel, securitySeverity string) string {
	if b := secSeverityBucket(securitySeverity); b != "" {
		return b
	}
	level := resultLevel
	if level == "" {
		level = ruleLevel
	}
	switch level {
	case "error":
		return "high"
	case "warning":
		return "medium"
	case "note", "none":
		return "low"
	}
	return ""
}

// secSeverityBucket maps a SARIF security-severity string (a CVSS-style 0.0
// to 10.0 score) to our buckets, the way GitHub code scanning does. "" when
// absent, unparseable, or non-positive (no meaningful score → fall back).
func secSeverityBucket(s string) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return ""
	}
	switch {
	case v >= 9.0:
		return "critical"
	case v >= 7.0:
		return "high"
	case v >= 4.0:
		return "medium"
	case v > 0:
		return "low"
	}
	return ""
}

// FailsAt says whether a finding of severity fails a scan whose failOn is
// failOn: every finding when failOn is empty, otherwise one ranked at or
// above it. It is the one meaning of a scan's failOn; a policy ceiling
// (wow.FailOnLooseness) orders thresholds by it.
func FailsAt(failOn, severity string) bool {
	return failOn == "" || SeverityRank(severity) >= SeverityRank(failOn)
}

// SeverityRank orders severities for failOn thresholds. Unknown ranks as
// high: a finding nobody scored is not a finding you may silently ship. A
// note was scored, as the lowest level a check declaration may take.
func SeverityRank(s string) int {
	switch s {
	case "note":
		return 0
	case "critical":
		return 4
	case "high", "unknown", "":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 3
}

// --- OSV normalizer ---------------------------------------------------------

// normalizeOSV parses osv-scanner JSON output. Fingerprint: vulnID + package
// NAME (not version — an acceptance survives bumps that don't fix it; if the
// bump fixes it, the finding disappears anyway).
func normalizeOSV(report []byte, source string) ([]Finding, error) {
	var doc struct {
		Results []struct {
			Packages []struct {
				Package struct {
					Name      string `json:"name"`
					Version   string `json:"version"`
					Ecosystem string `json:"ecosystem"`
				} `json:"package"`
				Vulnerabilities []struct {
					ID       string   `json:"id"`
					Aliases  []string `json:"aliases"`
					Summary  string   `json:"summary"`
					Details  string   `json:"details"`
					Severity []struct {
						Type  string `json:"type"`
						Score string `json:"score"`
					} `json:"severity"`
					DatabaseSpecific map[string]any `json:"database_specific"`
					Affected         []struct {
						Ranges []struct {
							Events []map[string]string `json:"events"`
						} `json:"ranges"`
					} `json:"affected"`
				} `json:"vulnerabilities"`
				Groups []struct {
					IDs                  []string `json:"ids"`
					MaxSeverity          string   `json:"max_severity"`
					ExperimentalAnalysis map[string]struct {
						Called bool `json:"called"`
					} `json:"experimental_analysis"`
				} `json:"groups"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.Unmarshal(report, &doc); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Finding
	for _, res := range doc.Results {
		for _, pkg := range res.Packages {
			// group max_severity (CVSS score as string) by vuln id
			groupSev := map[string]string{}
			reached := map[string]*bool{}
			for _, g := range pkg.Groups {
				for _, id := range g.IDs {
					groupSev[id] = g.MaxSeverity
				}
				for id, ea := range g.ExperimentalAnalysis {
					v := ea.Called
					reached[id] = &v
				}
			}
			for _, v := range pkg.Vulnerabilities {
				fp := "vuln|" + v.ID + "|" + pkg.Package.Name
				if seen[fp] {
					continue
				}
				seen[fp] = true
				f := Finding{
					Fingerprint: fp,
					Kind:        "vuln",
					Severity:    osvSeverity(v.Severity, groupSev[v.ID]),
					Identifiers: append([]string{v.ID}, v.Aliases...),
					Package: &PackageRef{Name: pkg.Package.Name, Version: pkg.Package.Version,
						Ecosystem: pkg.Package.Ecosystem, FixedIn: osvFixedIn(v.Affected, pkg.Package.Version)},
					Title:   v.Summary,
					Source:  source,
					Reached: reached[v.ID],
				}
				if f.Title == "" {
					f.Title = firstLine(v.Details)
				}
				if u, ok := v.DatabaseSpecific["url"].(string); ok {
					f.URL = u
				} else {
					f.URL = "https://osv.dev/vulnerability/" + v.ID
				}
				out = append(out, f)
			}
		}
	}
	return out, nil
}

type osvSeverityEntry = struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

func osvSeverity(entries []osvSeverityEntry, groupMax string) string {
	// Prefer the group's numeric max_severity; fall back to CVSS vector base
	// bucket; else unknown (Go stdlib advisories often carry no score).
	var score float64
	if _, err := fmt.Sscanf(groupMax, "%f", &score); err == nil && score > 0 {
		return cvssBucket(score)
	}
	for _, e := range entries {
		// Score may be a bare number or a CVSS vector; vectors are not
		// evaluated here — treat presence without a number as unknown.
		if _, err := fmt.Sscanf(e.Score, "%f", &score); err == nil && score > 0 {
			return cvssBucket(score)
		}
	}
	return "unknown"
}

func cvssBucket(score float64) string {
	switch {
	case score >= 9.0:
		return "critical"
	case score >= 7.0:
		return "high"
	case score >= 4.0:
		return "medium"
	case score > 0:
		return "low"
	}
	return "unknown"
}

// osvFixedIn picks the fix RELEVANT to the installed version: the smallest
// fixed event greater than it (multi-branch advisories list backport fixes
// for older branches first — 1.24.8 is not the answer for a 1.25 module).
// Falls back to the highest fixed event when none exceeds the version.
func osvFixedIn(affected []struct {
	Ranges []struct {
		Events []map[string]string `json:"events"`
	} `json:"ranges"`
}, currentVersion string) string {
	var all []string
	for _, a := range affected {
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if fx := e["fixed"]; fx != "" {
					all = append(all, fx)
				}
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	cur := dottedVersion(currentVersion)
	best, max := "", ""
	for _, fx := range all {
		v := dottedVersion(fx)
		if max == "" || versionLess(dottedVersion(max), v) {
			max = fx
		}
		if versionLess(cur, v) && (best == "" || versionLess(v, dottedVersion(best))) {
			best = fx
		}
	}
	if best != "" {
		return best
	}
	return max
}

func dottedVersion(s string) []int {
	var out []int
	for _, part := range strings.Split(strings.TrimPrefix(s, "v"), ".") {
		n := 0
		fmt.Sscanf(part, "%d", &n)
		out = append(out, n)
	}
	return out
}

func versionLess(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return strs.Truncate(s, 120)
}

// --- SARIF normalizer -------------------------------------------------------

// normalizeSARIF parses SARIF 2.1.0. kind comes from the binding manifest
// (a SARIF file doesn't know if its rules are secrets or SAST); severity
// from result level, defaulting by kind when absent (gitleaks omits level —
// a detected secret is high, full stop).
func normalizeSARIF(report []byte, source, kind string) ([]Finding, error) {
	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Name string `json:"name"`
					// SARIF puts a rule's severity here, not on each result
					// (this is where semgrep emits it); a result inherits it.
					Rules []struct {
						ID                   string `json:"id"`
						DefaultConfiguration struct {
							Level string `json:"level"`
						} `json:"defaultConfiguration"`
						Properties struct {
							SecuritySeverity string `json:"security-severity"`
						} `json:"properties"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Kind    string `json:"kind"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				Fingerprints        map[string]string `json:"fingerprints"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(report, &doc); err != nil {
		return nil, err
	}
	if kind == "" {
		kind = "sast"
	}
	seen := map[string]bool{}
	var out []Finding
	for _, run := range doc.Runs {
		// A result's severity is inherited from its rule when the result
		// carries no level of its own (SARIF 2.1.0 §3.27.10) — semgrep always
		// emits level on the rule, never the result. Index the rules by id.
		ruleLevel := map[string]string{}
		ruleSecSev := map[string]string{}
		for _, rd := range run.Tool.Driver.Rules {
			ruleLevel[rd.ID] = rd.DefaultConfiguration.Level
			ruleSecSev[rd.ID] = rd.Properties.SecuritySeverity
		}
		for _, r := range run.Results {
			// SARIF result.kind defaults to "fail"; pass/open/informational/
			// review/notApplicable are NOT detected problems — skip them.
			if r.Kind != "" && r.Kind != "fail" {
				continue
			}
			// result.suppressions (in-source `nosemgrep`, baselines) are
			// deliberately NOT honored: suppression in AppTown is an auditable
			// disposition carrying a justification, not a code comment the
			// scan can't see. Revisit if in-source suppression is wanted.
			var loc *Location
			file, line := "", 0
			if len(r.Locations) > 0 {
				pl := r.Locations[0].PhysicalLocation
				file = strings.TrimPrefix(pl.ArtifactLocation.URI, "/work/")
				line = pl.Region.StartLine
				loc = &Location{File: file, Line: line}
			}
			// Prefer tool-provided stable fingerprints; else rule+location
			// (accepting some churn when lines move — findings.md §8).
			fp := ""
			for _, m := range []map[string]string{r.Fingerprints, r.PartialFingerprints} {
				for _, k := range sortedKeys(m) {
					if m[k] != "" {
						fp = kind + "|" + r.RuleID + "|" + m[k]
						break
					}
				}
				if fp != "" {
					break
				}
			}
			if fp == "" {
				fp = fmt.Sprintf("%s|%s|%s|%d", kind, r.RuleID, file, line)
			}
			if seen[fp] {
				continue
			}
			seen[fp] = true
			sev := sarifSeverity(r.Level, ruleLevel[r.RuleID], ruleSecSev[r.RuleID])
			if sev == "" {
				// No severity anywhere in the SARIF. A detected secret is high
				// regardless (gitleaks emits no level); otherwise fail-safe to
				// unknown, which ranks as high — an unscored finding is not one
				// to ship silently (see SeverityRank).
				if kind == "secret" {
					sev = "high"
				} else {
					sev = "unknown"
				}
			}
			f := Finding{
				Fingerprint: fp, Kind: kind, Severity: sev,
				Identifiers: []string{r.RuleID},
				Location:    loc,
				Title:       fmt.Sprintf("%s: %s", run.Tool.Driver.Name, r.RuleID),
				Detail:      r.Message.Text,
				Source:      source,
			}
			// Trivy says which package, in the message: "Package: libc6 /
			// Installed Version: … / Fixed Version: …". Lift it so the
			// findings table says what to bump, not just a CVE id.
			if pkg := SARIFMessageField(r.Message.Text, "Package"); pkg != "" {
				f.Package = &PackageRef{Name: pkg,
					Version: SARIFMessageField(r.Message.Text, "Installed Version"),
					FixedIn: SARIFMessageField(r.Message.Text, "Fixed Version")}
				f.Title = fmt.Sprintf("%s in %s %s", r.RuleID, pkg, f.Package.Version)
				if f.Package.FixedIn != "" {
					f.Title += " (fixed in " + f.Package.FixedIn + ")"
				}
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Normalize turns a scanner's native report into findings. The error says why
// nothing came back — an unknown format, or a report that would not parse;
// the gate ignores it the way it always has (no findings is the answer either
// way) and a self-service run can say what went wrong.
func Normalize(format string, report []byte, source, kind string) ([]Finding, error) {
	switch format {
	case "osv":
		if err := envelope(report, "results"); err != nil {
			return nil, fmt.Errorf("not an osv report: %w", err)
		}
		return normalizeOSV(report, source)
	case "sarif":
		if err := envelope(report, "runs"); err != nil {
			return nil, fmt.Errorf("not a SARIF report: %w", err)
		}
		return normalizeSARIF(report, source, kind)
	}
	return nil, fmt.Errorf("no normalizer for report format %q", format)
}

// envelope checks a report has its format's shape: a JSON object whose
// list field is present and a list (empty when nothing was found). JSON
// that is not such a report (null, a scanner's error object) says nothing
// about findings, and is never read as a clean report.
func envelope(report []byte, list string) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(report, &top); err != nil {
		return err
	}
	if top == nil {
		return errors.New("it is not an object")
	}
	raw, ok := top[list]
	if !ok {
		return fmt.Errorf("it has no %q", list)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return fmt.Errorf("its %q is not a list", list)
	}
	return nil
}

// SARIFMessageField: "Label: value" out of a multi-line SARIF message.
func SARIFMessageField(text, label string) string {
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), label+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
