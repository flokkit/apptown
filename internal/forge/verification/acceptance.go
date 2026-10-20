package verification

import (
	"sort"
	"strings"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
)

// unmetAcceptance names every acceptance check whose result is not a pass.
func unmetAcceptance(results Results, target workflow.AcceptanceScope) []string {
	want := map[string]bool{}

	for _, k := range target.Keys() {
		want[k] = true
	}
	if len(want) == 0 {
		return nil
	}
	seen := map[string]workflow.CheckVerdict{}
	for _, r := range results {
		seen[r.Block+"|"+r.Check] = r.Verdict
	}
	var out []string
	for k := range want {
		v, ok := seen[k]
		if !ok {
			out = append(out, k+": not evaluated")
		} else if v != workflow.VerdictPass {
			out = append(out, k+": "+v.String())
		}
	}
	sort.Strings(out)
	return out
}

// findingEvaluator recovers which evaluator produced a finding by matching
// its fingerprint back to the per-check results.
func findingEvaluator(results Results, f conformance.Finding) string {
	for _, r := range results {
		if matchesFinding(r, f) {
			return r.Evaluator
		}
	}
	return ""
}

// onlyForgeFaults reports whether every result the acceptance names as unmet
// was one AppTown could not judge for its own reasons (no preview to
// probe). Such a gate errors and waits for a person instead of reworking
// the change: rework for AppTown's own fault invites unrelated changes.
func onlyForgeFaults(results Results, unmet []string) bool {
	if len(unmet) == 0 {
		return false
	}
	faults := map[string]bool{}
	for _, r := range results {
		if r.ForgeFault {
			faults[r.Block+"|"+r.Check] = true
		}
	}
	for _, u := range unmet {
		key := strings.SplitN(u, ": ", 2)[0]
		if !faults[key] {
			return false
		}
	}
	return true
}
