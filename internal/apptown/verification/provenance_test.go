package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
)

func TestSplitByProvenanceUnavailableBaseGatesEverything(t *testing.T) {
	t.Parallel()
	fs := []conformance.Finding{{Finding: check.Finding{Fingerprint: "x", Kind: "sast", Identifiers: []string{"r"}, Location: &check.Location{File: "a.go"}}}}
	intro, carried := splitByProvenance(fs, scanBase{OK: false, Note: "no base", Findings: []check.Finding{fs[0].Finding}})
	if len(intro) != 1 || len(carried) != 0 {
		t.Fatalf("an unavailable base must not hide findings: %d/%d", len(intro), len(carried))
	}
}

// A scanner finding is carried only when the base reports its fingerprint,
// one base finding for one head finding. A different finding of the same
// rule in the same file is the change's: fixing an old issue and adding
// another of its rule must not let the new one pass as debt.
//
// Kills: the second, loose match on kind, rule and file (fallback audit
// F03), which carried "new" on the base's "old".
func TestAScanFindingIsCarriedOnlyByItsOwnFingerprint(t *testing.T) {
	t.Parallel()
	at := func(fp string) conformance.Finding {
		return conformance.Finding{Finding: check.Finding{Fingerprint: fp, Kind: "sast", Identifiers: []string{"r"}, Location: &check.Location{File: "a.go"}}}
	}
	base := func(fps ...string) scanBase {
		b := scanBase{OK: true}
		for _, fp := range fps {
			b.Findings = append(b.Findings, at(fp).Finding)
		}
		return b
	}
	cases := []struct {
		name           string
		head           []conformance.Finding
		base           scanBase
		intro, carried int
	}{
		{"the old finding fixed, a new one of its rule in its file", []conformance.Finding{at("new")}, base("old"), 1, 0},
		{"the old finding kept, and a new one", []conformance.Finding{at("new"), at("old")}, base("old"), 1, 1},
		{"two old findings, both by fingerprint", []conformance.Finding{at("a"), at("b")}, base("a", "b"), 0, 2},
		{"one base finding carries one head finding", []conformance.Finding{at("a"), at("a")}, base("a"), 1, 1},
	}
	for _, c := range cases {
		intro, carried := splitByProvenance(c.head, c.base)
		if len(intro) != c.intro || len(carried) != c.carried {
			t.Errorf("%s: introduced %d, carried %d; want %d, %d", c.name, len(intro), len(carried), c.intro, c.carried)
		}
	}
}

// A scan step matches its whole scan against the base before applying
// exceptions. An excepted finding still accounts for its own occurrence on
// main, so a new finding of its rule in its file is the change's and gates,
// whichever order the scanner reports them in.
//
// Kills: dropping excepted findings before the comparison, which left the
// excepted finding's base occurrence free to carry the new one.
func TestAnExceptedFindingDoesNotCarryANewOneOfItsRule(t *testing.T) {
	t.Parallel()
	at := func(fp string) conformance.Finding {
		return conformance.Finding{Finding: check.Finding{Fingerprint: fp, Kind: "sast", Severity: "high", Identifiers: []string{"r"}, Location: &check.Location{File: "a.go"}}}
	}
	base := scanBase{OK: true, Findings: []check.Finding{at("A").Finding}}
	accepted := []conformance.Disposition{{Fingerprint: "A", Status: conformance.AcceptedRisk}}
	for _, head := range [][]conformance.Finding{{at("A"), at("B")}, {at("B"), at("A")}} {
		actionable, gating, carried := scanGate(head, accepted, base, true)
		if len(actionable) != 1 || len(gating) != 1 || gating[0].Fingerprint != "B" || len(carried) != 0 {
			t.Fatalf("head %s,%s: actionable %d, gating %+v, carried %+v; want only B gating",
				head[0].Fingerprint, head[1].Fingerprint, len(actionable), gating, carried)
		}
	}
	// No comparison: every finding without an exception gates.
	if _, gating, carried := scanGate([]conformance.Finding{at("A"), at("B")}, accepted, scanBase{}, false); len(gating) != 1 || len(carried) != 0 {
		t.Fatalf("without a base: %+v / %+v", gating, carried)
	}
}
