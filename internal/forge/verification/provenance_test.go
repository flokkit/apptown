package verification

import (
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
)

func TestSplitByProvenanceUnavailableBaseGatesEverything(t *testing.T) {
	t.Parallel()
	fs := []conformance.Finding{{Finding: check.Finding{Fingerprint: "x", Kind: "sast", Identifiers: []string{"r"}, Location: &check.Location{File: "a.go"}}}}
	intro, carried := splitByProvenance(fs, scanBase{OK: false, Note: "no base"})
	if len(intro) != 1 || len(carried) != 0 {
		t.Fatalf("an unavailable base must not hide findings: %d/%d", len(intro), len(carried))
	}
	base := scanBase{OK: true, Findings: []check.Finding{{Fingerprint: "moved", Kind: "sast",
		Identifiers: []string{"r"}, Location: &check.Location{File: "a.go"}}}}
	intro, carried = splitByProvenance(fs, base)
	if len(intro) != 0 || len(carried) != 1 || !carried[0].PreExisting {
		t.Fatalf("loose match marks the finding pre-existing: %+v / %+v", intro, carried)
	}
}

// One finding on the base covers one finding of the change, never several:
// a second finding of the same rule in the same file, with a fingerprint the
// base never reported, is the change's and gates.
//
// Kills: a base matched as a set of rule-and-file keys, which let one old
// finding carry any number of new ones of its rule in its file.
func TestSplitByProvenanceOneBaseFindingCarriesOneFinding(t *testing.T) {
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
		{"a new finding beside a moved old one", []conformance.Finding{at("moved"), at("new")}, base("old"), 1, 1},
		{"the old finding by its fingerprint, and a new one", []conformance.Finding{at("new"), at("old")}, base("old"), 1, 1},
		{"two old findings, both by fingerprint", []conformance.Finding{at("a"), at("b")}, base("a", "b"), 0, 2},
		{"an exact match does not also spend a loose one", []conformance.Finding{at("a"), at("moved")}, base("a", "b"), 0, 2},
	}
	for _, c := range cases {
		intro, carried := splitByProvenance(c.head, c.base)
		if len(intro) != c.intro || len(carried) != c.carried {
			t.Errorf("%s: introduced %d, carried %d; want %d, %d", c.name, len(intro), len(carried), c.intro, c.carried)
		}
	}
	// The exact match wins over the loose one whatever the order: "new" must
	// not take the base finding that "old" names exactly.
	intro, _ := splitByProvenance([]conformance.Finding{at("new"), at("old")}, base("old"))
	if len(intro) != 1 || intro[0].Fingerprint != "new" {
		t.Fatalf("the finding with no base fingerprint is the change's: %+v", intro)
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
