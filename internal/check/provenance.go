package check

import (
	"fmt"
	"strings"
)

// Provenance: how a finding on the head tree reads against the base tree the
// change forked from (design/subsystems/ways-of-working-versions.md §4.2).
// A change is judged on what IT did; what already failed on the base is the
// estate's. Which commit is the base, and whether it is on the protected
// branch, are facts the caller establishes and passes in.
type Provenance int

const (
	Introduced  Provenance = iota // the change introduced it, or the base cannot say otherwise
	PreExisting                   // every occurrence was already on the base
	Undecided                     // fails on both trees with no occurrence evidence, or the base could not be judged: unknowable here
)

func (p Provenance) String() string {
	switch p {
	case PreExisting:
		return "preexisting"
	case Undecided:
		return "undecided"
	}
	return "introduced"
}

// judgeOccurrences classifies one head finding against the occurrences the
// same check reported on the base. A check that reports occurrences is judged
// by them: the occurrences not on the base are the change's, the rest are not,
// and the finding is rewritten to name only what is new. A boolean check —
// or a lines check that reported nothing — cannot say whether the change made
// it worse; that is Undecided, and a reviewer judges the diff.
//
// hasOccurrences says the declaration asks for them; base is what the same
// fingerprint reported on the base tree. The caller has already established
// that the check fails on the base too.
func judgeOccurrences(f *Finding, hasOccurrences bool, baseItems []string) Provenance {
	if !hasOccurrences || len(f.Items) == 0 {
		// A boolean check, or a lines check that reported nothing: no
		// occurrence evidence, so no claim of no regression.
		return Undecided
	}
	// Multiset matching: one base occurrence covers one head occurrence,
	// never several.
	base := map[string]int{}
	for _, it := range baseItems {
		base[it]++
	}
	var fresh []string
	for _, it := range f.Items {
		if base[it] > 0 {
			base[it]--
			continue
		}
		fresh = append(fresh, it)
	}
	if len(fresh) == 0 {
		return PreExisting
	}
	// Only what is new is the change's: the detail says so.
	f.Detail = fmt.Sprintf("%d new (%d already on main): %s", len(fresh), len(f.Items)-len(fresh), strings.Join(fresh, "; "))
	f.Items = fresh
	return Introduced
}

// wholeProperty says a check judges one property of the whole app and lists
// no occurrences: a runtime probe, or a model's rubric verdict.
func wholeProperty(d Declaration) bool {
	e := Evaluator(d)
	return (e == "runtime" || e == "llm") && d.Occurrences != "lines"
}

// carriedScans says which scanner findings of head (those of no declared
// check) the base already reported: the same fingerprint, one base finding
// for one head finding, never several. The fingerprint is the finding's
// identity; a finding the base does not report under it is new, so an old
// finding fixed and a different one of its rule introduced in its file is
// the change's (fallback audit F03). A scanner that gives no stable
// identity is identified by rule, file and line (normalizeSARIF), so a
// moved line reads as new: it blocks, never admits.
// Keys are [evaluation, finding] indexes into head.
func carriedScans(head, base []Evaluation) map[[2]int]bool {
	left := map[string]int{}
	for _, ev := range base {
		for _, f := range ev.Findings {
			if f.Fingerprint != "" {
				left[f.Fingerprint]++
			}
		}
	}
	out := map[[2]int]bool{}
	for i, ev := range head {
		if ev.Name != "" {
			continue
		}
		for j, f := range ev.Findings {
			if f.Fingerprint != "" && left[f.Fingerprint] > 0 {
				left[f.Fingerprint]--
				out[[2]int{i, j}] = true
			}
		}
	}
	return out
}

// Judged is one head evaluation with its provenance: the findings it carries
// as the base leaves them (an introduced finding names only what is new),
// split into what this tree introduced and what the base already reported. A
// finding that can be neither — a boolean check failing on both trees, with no
// occurrences to compare — is in neither list, and the provenance says
// Undecided: a reader judges the diff.
type Judged struct {
	Evaluation
	Provenance  Provenance
	Own         []Finding
	PreExisting []Finding
}

// Classify judges every head evaluation against what the base tree reported.
// One rule serves both kinds of finding, because the difference is in what a
// fingerprint identifies:
//
//   - A DECLARED CHECK's finding stands for the whole check, so the same
//     fingerprint on the base says only "this check fails there too". The
//     occurrences decide: the ones not on the base are this tree's, and a
//     check with no occurrences to compare is Undecided.
//
//   - Any other finding — a scanner's — identifies the problem itself, so
//     the base reporting the same fingerprint is proof the problem was
//     already there (carriedScans).
//
//   - A check that judges one property of the whole app and reports no
//     occurrences — a probe of the running app, a model's verdict on a
//     rubric — is PreExisting when the base fails it too: the property was
//     already broken there, and there is nothing to count that the change
//     could have added to. (A shell check without occurrences stays
//     Undecided: its author could have asked it to list what it found.)
//
//   - A declared check the base could not be judged on (a base evaluation of
//     it that is cannot-assess: main's deployment runs another commit, the
//     model did not answer) leaves its head finding Undecided: nothing says
//     whether the change introduced it, and the caller names why.
//
// baseKnown says the base was evaluated at all: without it nothing can be
// subtracted and every finding is the head's (Introduced). What the base
// commit is, whether it is on the protected branch, and which checks are
// judged absolutely whatever the base says, are the caller's facts —
// established before the base evaluations are handed over.
func Classify(head, base []Evaluation, baseKnown bool) []Judged {
	exact := map[string]bool{}
	items := map[string][]string{}
	unassessed := map[string]bool{} // checks the base could not be judged on
	for _, ev := range base {
		if ev.Name != "" && ev.Outcome == CannotAssess {
			unassessed[Fingerprint(ev.Identity, ev.Name, "")] = true
		}
		for _, f := range ev.Findings {
			exact[f.Fingerprint] = true
			items[f.Fingerprint] = append(items[f.Fingerprint], f.Items...)
		}
	}
	scans := carriedScans(head, base)
	out := make([]Judged, 0, len(head))
	for hi, ev := range head {
		j := Judged{Evaluation: ev}
		j.Findings = append([]Finding(nil), ev.Findings...) // the head's own slice is not rewritten
		undecided := false
		for i, f := range ev.Findings {
			p := Introduced
			switch {
			case !baseKnown:
			case ev.Name != "": // a declared check: judged by its occurrences
				switch {
				case exact[f.Fingerprint] && wholeProperty(ev.Declaration):
					p = PreExisting
				case exact[f.Fingerprint]:
					p = judgeOccurrences(&f, ev.Declaration.Occurrences == "lines", items[f.Fingerprint])
				case unassessed[Fingerprint(ev.Identity, ev.Name, "")]:
					p = Undecided
				}
			case scans[[2]int{hi, i}]:
				p = PreExisting
			}
			j.Findings[i] = f // an introduced finding names only what is new
			switch p {
			case PreExisting:
				j.PreExisting = append(j.PreExisting, f)
			case Undecided:
				undecided = true
			default:
				j.Own = append(j.Own, f)
			}
		}
		switch {
		case len(j.Own) > 0 || (len(j.PreExisting) == 0 && !undecided):
			j.Provenance = Introduced
		case len(j.PreExisting) > 0:
			j.Provenance = PreExisting
		default:
			j.Provenance = Undecided
		}
		out = append(out, j)
	}
	return out
}
