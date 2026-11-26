package workflowexec

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

// ScopeResult is feedback on one commit, never evidence for a gate.
type ScopeResult struct {
	Commit string             `json:"commit"`
	Status workflow.StepState `json:"status"`
	Brief  string             `json:"brief"`
	Reason string             `json:"reason"`
	Facts  string             `json:"facts"`
}

// Scope applies the run's admitted routine judgement to a named change commit.
// It changes no step, event, proposal or hold. The final routine judges again.
func (a *commands) Scope(ctx context.Context, runID ids.ID, commit string) (ScopeResult, error) {
	var out ScopeResult
	run, err := workflow.ReadRunIn(ctx, a.db, runID)
	if err != nil {
		return out, err
	}
	spec, err := run.Definition()
	if err != nil {
		return out, err
	}
	var name, rubric string
	for _, stage := range spec.Stages {
		for _, step := range append(stage.OnEnter, stage.Steps...) {
			if step.Uses == RoutineKind {
				name = step.Name
				rubric, _ = step.With["rubric"].(string)
			}
		}
	}
	if name == "" {
		return out, fault.Wrap(fault.NotFound, fmt.Errorf("this run has no routine step"))
	}
	kind, err := routineKind(run)
	if err != nil {
		return out, err
	}
	ch, err := workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
	if err != nil {
		return out, err
	}
	if commit == "" {
		return out, fault.Wrap(fault.Invalid, fmt.Errorf("commit is required"))
	}
	repo, sha, _, err := verification.CheckedCommit(ctx, a.db, a.cfg.DataDir, ch, verification.Selection{Commit: commit})
	if err != nil {
		return out, err
	}
	out.Commit = sha
	if kind.Classifier == fix.AdoptClassifier {
		out.Status, out.Reason = workflow.StatusNotApplicable, "dependency settings do not apply to this run"
		return out, nil
	}
	if kind.Classifier != fix.UpdateClassifier {
		return out, fmt.Errorf("unsupported fix classifier %q", kind.Classifier)
	}
	p, err := a.prepareScope(ctx, run, repo, sha)
	if err != nil {
		return out, err
	}
	out.Brief = p.Brief
	j := p.Early
	if j == nil {
		judged, err := a.judgeScope(ctx, run, name, rubric, p)
		if err != nil {
			return out, err
		}
		j = &judged
	}
	if j.Unreadable {
		return out, fault.Wrap(fault.Invalid, fmt.Errorf("%s", j.Reason))
	}
	if j.Status == workflow.StatusError {
		return out, fault.Wrap(fault.Unavailable, fmt.Errorf("%s", j.Reason))
	}
	out.Status, out.Reason, out.Facts = j.Status, j.Reason, j.Facts
	return out, nil
}
