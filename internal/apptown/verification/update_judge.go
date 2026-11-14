package verification

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

// JudgeUpdate uses the checks slot and the same isolated diff-rubric evaluator
// as guidance. Its receipt gates standing acceptance rather than calibration.
func (a *Guidance) JudgeUpdate(ctx context.Context, source workflow.Source, item check.Item, diff string) (workflow.CheckReceipt, error) {
	by, meta, err := a.judgeChecks(ctx, source, []check.Item{item}, diff)
	if err != nil {
		return workflow.CheckReceipt{}, err
	}
	v, answered := by[1]
	ev := check.Judgement(item, v, answered, check.Gate)
	receipt := resultOf(ev)
	receipt.Detail = ev.Detail
	receipt.Evaluator, receipt.Scope = "routine", check.ScopeDiff
	receipt.Evidence, receipt.Note = v.Evidence, v.Note
	if meta != nil {
		r := meta.Results[1]
		receipt.AgentEvidence = &r.Evidence
		receipt.AppTownFault = r.Evidence.AppTownFault
		receipt.EvaluatorVer = agentEvaluatorVersion(r.Evidence)
		if r.Reason != "" {
			receipt.Detail = r.Reason
		}
	}
	if receipt.Verdict != workflow.VerdictPass && receipt.Verdict != workflow.VerdictFail {
		receipt.Verdict = workflow.VerdictError
	}
	return receipt, nil
}
