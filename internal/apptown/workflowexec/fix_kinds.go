package workflowexec

import (
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fix"
)

func (a *commands) RunFix(task *workflow.Execution) {
	var cfg fixConfig
	if err := workflow.DecodeStepConfig(task.Step().Config, &cfg); err != nil {
		fixKindError(task, err)
		return
	}
	handler, ok := fix.Lookup(cfg.Kind)
	if !ok {
		fixKindError(task, fmt.Errorf("unknown fix kind %q", cfg.Kind))
		return
	}
	switch handler.Action {
	case fix.AdoptAction:
		a.runAdopt(task, false)
	case "":
		if _, err := task.Claim(); err != nil {
			return
		}
		targets, err := runTargets(task.Run())
		if err == nil {
			err = task.StartFix(targets)
		}
		if err != nil {
			task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": err.Error()}, nil))
			return
		}
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "this pack version's dependency update is no longer supported; adopt the current platform pack"}, nil))
	default:
		fixKindError(task, fmt.Errorf("unsupported fix action %q", handler.Action))
	}
}

func (a *commands) RunRoutine(task *workflow.Execution) {
	spec, err := task.Run().Definition()
	if err != nil {
		fixKindError(task, err)
		return
	}
	kind := "update"
	for _, stage := range spec.Stages {
		for _, step := range append(stage.OnEnter, stage.Steps...) {
			if step.Uses != FixKind {
				continue
			}
			selected, _ := step.With["kind"].(string)
			if kind != "update" && kind != selected {
				fixKindError(task, fmt.Errorf("routine needs one fix kind in its run"))
				return
			}
			kind = selected
		}
	}
	handler, ok := fix.Lookup(kind)
	if !ok {
		fixKindError(task, fmt.Errorf("routine has no supported fix kind in its run"))
		return
	}
	switch handler.Classifier {
	case fix.AdoptClassifier:
		a.runAdopt(task, true)
	case fix.UpdateClassifier:
		a.runUpdateRoutine(task)
	default:
		fixKindError(task, fmt.Errorf("unsupported fix classifier %q", handler.Classifier))
	}
}
func fixKindError(task *workflow.Execution, err error) {
	if _, claimErr := task.Claim(); claimErr == nil {
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": err.Error()}, nil))
	}
}
