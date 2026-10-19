package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
)

// The baseline (design/subsystems/conformance.md): a change is
// judged on what IT did. A deterministic finding that already fails on the
// change's base — main as the change forked from it — is the estate's
// finding, not the change's: it is reported as "already on main", it does
// not fail the gate, and it is never fed back to the agent as work. The
// fleet review opens its own remediation for it. The one exception is a
// remediation change, which exists to close a named check: that check is
// judged absolutely, base or no base.
//
// Without this, adopting a strict block turns every ordinary change into a
// forced migration: a gofmt remediation told to "fix or disposition" an
// unrelated database finding may port the app to another database.

// baselineSet is what the base tree reports: per fingerprint, whether the
// check fails there and which occurrences it reported; plus the checks that
// could not be judged there (unknown, so never subtracted).
type baselineSet struct {
	failing map[string]bool
	items   map[string][]string // fingerprint → occurrences on the base
	unknown map[string]bool     // "block@v|check" the base could not evaluate
	sha     string
	onMain  bool // the base is an ancestor of the protected branch (provenance, §4.2)
	isMain  bool // the base IS the protected branch's head (a landing's baseline)
	note    string
	at      time.Time
}

// unknownRetry: a baseline with unknowns (an image not there, an evaluator
// error) is asked again after this long rather than cached for good.
const unknownRetry = 2 * time.Minute

type baselineCache struct {
	mu      sync.Mutex
	entries map[string]*baselineSet
}

// A landing is compared with current main; working runs prefer the change's
// recorded base. Known recorded bases remain usable when Git has no fork proof.
func workflowBaseline(ctx context.Context, dataDir string, source workflow.Source) string {
	if source.ChangeID.IsZero() {
		return ""
	}
	if source.RootRole == "landing" && source.RepoBacked() {
		if head, err := (deploy.SelectedSource{Repository: source.Repository(dataDir), Revision: "refs/heads/" + source.DefaultBranch}).Resolve(ctx); err == nil {
			return head
		}
	}
	return strs.FirstNonBlank(source.ChangeBaseSHA, workflowForkPoint(ctx, dataDir, source))
}

func checksDigest(checks []conformance.DeclaredCheck) string {
	var sb strings.Builder
	for _, sc := range checks {
		fmt.Fprintf(&sb, "%s@%d|%s|%s;", sc.Block, sc.Version, sc.Check.Name, check.Evaluator(sc.Check))
	}
	return shortHash(sb.String())
}

// evaluations reads what the base tree reported as evaluations the library can
// judge against: one per failing check, carrying the occurrences it reported
// there. A check a remediation exists to close is left out — its target is the
// change's absolutely, base or no base (design/ways-of-working-versions.md §4.2).
func (b *baselineSet) evaluations(target workflow.AcceptanceScope) []check.Evaluation {
	if b == nil {
		return nil
	}
	out := make([]check.Evaluation, 0, len(b.failing))
	for fingerprint := range b.failing {
		if target.Covers(fingerprint) {
			continue
		}
		out = append(out, check.Evaluation{Outcome: check.DoesNotConform,
			Findings: []check.Finding{{Fingerprint: fingerprint, Items: b.items[fingerprint]}}})
	}
	return out
}

const (
	notePreexisting = "already fails on main, nothing new here — not this change's to fix; the fleet review opens its own remediation"
	noteUndecided   = "fails on main too; the check reports no occurrences, so AppTown cannot tell whether this change made it worse — the reviewer judges the diff"
)

// headEvaluations reads each head finding as the evaluation that produced it:
// the library judges provenance over evaluations, and the check a finding
// belongs to is what says whether it reports occurrences.
func headEvaluations(findings []conformance.Finding, checks []conformance.DeclaredCheck) []check.Evaluation {
	out := make([]check.Evaluation, 0, len(findings))
	for _, f := range findings {
		out = append(out, check.Evaluation{Declared: declaredFor(checks, f), Findings: []check.Finding{f.Finding}})
	}
	return out
}

// declaredFor recovers the adopted check a finding belongs to: one exact
// block, version and name (matchesFinding). A finding whose check
// is no longer in the set is still a check's — named by its own fingerprint,
// with no occurrences to subtract.
func declaredFor(checks []conformance.DeclaredCheck, f conformance.Finding) check.Declared {
	for _, sc := range checks {
		if strings.HasPrefix(f.Fingerprint, fmt.Sprintf("guidance|%s@%d|%s|", sc.Block, sc.Version, sc.Check.Name)) {
			return sc.Declared()
		}
	}
	return check.Declared{Declaration: check.Declaration{Name: f.Fingerprint}}
}

// classifyAgainstBase splits the head findings three ways and annotates
// the per-check results: what the change introduced (own), what was
// already on the base (preexisting), and what cannot be decided. The rule is
// the library's (check.Classify); the facts it judges on — which tree is the
// base, and which checks a remediation is opened to close — are forge's.
//
// judged are the base evaluations made for this comparison alone, beside
// the cached deterministic baseline: the model rubrics judged on the base
// tree, and the runtime probes of main's deployment when it serves the base
// (or, when it does not, why the base could not be judged — those findings
// are undecided, and say why).
func classifyAgainstBase(results Results, findings []conformance.Finding, checks []conformance.DeclaredCheck, b *baselineSet, target workflow.AcceptanceScope, judged ...check.Evaluation) (own, preexisting, undecided []conformance.Finding) {
	base := b.evaluations(target)
	why := map[string]string{} // check fingerprint → why the base could not be judged on it
	for _, ev := range judged {
		fp := check.Fingerprint(ev.Identity, ev.Name, "")
		if target.Covers(fp) {
			continue // a remediation's target is judged absolutely
		}
		if ev.Outcome == check.CannotAssess {
			why[fp] = ev.Detail
		}
		base = append(base, ev)
	}
	annotate := func(f conformance.Finding, provenance check.Provenance, note string) {
		for i := range results {
			r := &results[i]
			if matchesFinding(*r, f) {
				if provenance == check.PreExisting {
					r.Preexisting = true
				}
				if provenance == check.Undecided {
					r.Undecided = true
				}
				r.Note = note
			}
		}
	}
	for i, j := range check.Classify(headEvaluations(findings, checks), base, b != nil) {
		f := findings[i]
		f.Finding = j.Findings[0] // an introduced finding names only what is new
		p := j.Provenance
		f.Regression = p.String()
		switch p {
		case check.PreExisting:
			preexisting = append(preexisting, f)
			annotate(f, p, notePreexisting)
		case check.Undecided:
			note := noteUndecided
			if reason := why[f.Fingerprint]; reason != "" {
				// The finding itself carries the reason, so every view of it
				// (the step's evidence, the reviewer's brief) says why.
				note = "could not be compared with main: " + reason + " — shown, not blocking; the reviewer judges the diff"
				f.Detail = strings.TrimSpace(f.Detail + " (undecided: " + reason + ")")
			}
			undecided = append(undecided, f)
			annotate(f, p, note)
		default:
			own = append(own, f)
		}
		findings[i] = f // the artifact carries the split (§4.2)
	}
	return own, preexisting, undecided
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
