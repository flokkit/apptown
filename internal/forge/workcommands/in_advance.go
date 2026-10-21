package workcommands

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// InAdvance answers what a yes given in advance on an adoption of the
// project would mean, from the workflows its way of working pins now.
func (l LandingSentences) InAdvance(ctx context.Context, projectID ids.ID) (workflow.InAdvanceQuestion, error) {
	record, err := project.ByIDIn(ctx, l.DB, projectID)
	if err != nil {
		return workflow.InAdvanceQuestion{}, err
	}
	return inAdvanceQuestion(ctx, l.Lanes, &record)
}

// inAdvanceQuestion reads the lane an adoption Change runs (the one
// Uplift starts) and the landing: the sentence each of their approvals
// gives a yes in advance. The lane's acceptance is its last stage's one
// approval; an approval that gives no such sentence means none is offered.
func inAdvanceQuestion(ctx context.Context, lanes remediation.WorkflowResolver, p *project.Project) (workflow.InAdvanceQuestion, error) {
	q := workflow.InAdvanceQuestion{Landing: []string{}}
	lane := remediation.WorkflowFor(ctx, lanes, p)
	if lane == "" {
		q.NotOffered = "this project has no quick-change or implement workflow to work an adoption on"
		return q, nil
	}
	q.Workflow = lane
	working, err := lanes.SelectedWorkflow(ctx, p, lane)
	if err != nil {
		return q, err
	}
	stages := working.Doc.Spec.Stages
	var accepts []schema.Step
	if len(stages) > 0 {
		last := stages[len(stages)-1]
		for _, step := range append(append([]schema.Step{}, last.OnEnter...), last.Steps...) {
			if step.Uses == "approval" {
				accepts = append(accepts, step)
			}
		}
	}
	if len(accepts) != 1 {
		q.NotOffered = fmt.Sprintf("%s v%d has no single acceptance to give a yes in advance to", lane, working.Version)
		return q, nil
	}
	if q.Acceptance, _ = accepts[0].With["inAdvance"].(string); q.Acceptance == "" {
		q.NotOffered = fmt.Sprintf("%s v%d's %s approval does not say what a yes given in advance means", lane, working.Version, accepts[0].Name)
		return q, nil
	}
	landing, err := lanes.SelectedWorkflow(ctx, p, "land")
	if err != nil {
		return q, err
	}
	for _, stage := range landing.Doc.Spec.Stages {
		for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
			if step.Uses != "approval" {
				continue
			}
			s, _ := step.With["inAdvance"].(string)
			if s == "" {
				q.NotOffered = fmt.Sprintf("the landing (%s v%d)'s %s approval does not say what a yes given in advance means", landing.Name, landing.Version, step.Name)
				return q, nil
			}
			q.Landing = append(q.Landing, s)
		}
	}
	return q, nil
}
