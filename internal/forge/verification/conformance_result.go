package verification

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
)

// Results are the receipts of one evaluation run. The receipt is workflow's
// (CheckReceipt), because owners other than verification judge it.
type Results []workflow.CheckReceipt

// resultOf is the receipt an evaluation leaves: what the judge said,
// written in the words the web, the CLI and the reviewer read. Every
// evaluator's receipt is made here, so the shape cannot drift between them.
//
// The words with no outcome behind them are the App's, set by the caller from
// what only AppTown knows: "skipped" (there was nothing to judge with),
// "unclear" (the model answered but would not say), "excepted" (a person has
// already excused what was found). The library has no such outcomes and must
// not: they are decisions, not judgements.
func resultOf(ev check.Evaluation) workflow.CheckReceipt {
	res := workflow.CheckReceipt{
		Block: ev.Namespace, Version: ev.Version, Check: ev.Name,
		Evaluator:    check.Evaluator(ev.Declaration),
		ExitCode:     ev.Evidence.ExitCode,
		Output:       ev.Evidence.Output,
		Occurrences:  ev.Occurrences,
		EvaluatorVer: ev.Evidence.EvaluatorVersion,
		EvidenceHash: ev.Evidence.Hash,
		Files:        ev.Evidence.Files,
		ForgeFault:   ev.Fault,
		Image:        ev.Evidence.Image,
	}
	switch ev.Outcome {
	case check.Conforms:
		res.Verdict = workflow.VerdictPass
	case check.DoesNotConform:
		res.Verdict = workflow.VerdictFail
	case check.Error:
		res.Verdict = workflow.VerdictError
	default:
		res.Verdict = workflow.VerdictCannotAssess
	}
	return res
}

func detailOr(r workflow.CheckReceipt, fallback string) string {
	if strings.TrimSpace(r.Output) != "" {
		return r.Output
	}
	return fallback
}

// A finding belongs to one exact adopted check version. A same-named check
// from another block or version must not inherit its evaluator or baseline.
func matchesFinding(r workflow.CheckReceipt, f conformance.Finding) bool {
	return r.Check != "" && strings.HasPrefix(f.Fingerprint, fmt.Sprintf("guidance|%s@%d|%s|", r.Block, r.Version, r.Check))
}
