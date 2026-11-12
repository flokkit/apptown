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
	case fix.ScriptAction:
		a.RunScript(task)
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
	kind := ""
	for _, stage := range spec.Stages {
		for _, step := range append(stage.OnEnter, stage.Steps...) {
			if step.Uses != FixKind {
				continue
			}
			selected, _ := step.With["kind"].(string)
			if kind != "" && kind != selected {
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
