package workcommands

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// InAdvance reads the candidate's publication procedure and the project's
// landing procedure, which the landing resolves when it starts.
func (l LandingSentences) InAdvance(ctx context.Context, c adoption.Candidate) (workflow.InAdvanceQuestion, error) {
	record, err := project.ByIDIn(ctx, l.DB, c.ProjectID)
	if err != nil {
		return workflow.InAdvanceQuestion{}, err
	}
	working, _, err := adoptionProcedure(ctx, l.Adoption, c)
	if err != nil {
		return workflow.InAdvanceQuestion{}, err
	}
	landing, err := l.Adoption.Workflow(ctx, adoption.WorkflowRequest{Source: adoption.Source{ProjectID: c.ProjectID, Revision: &adoption.RecordedRevision{ProjectID: c.ProjectID, Receipt: c.Receipt}}, Name: "land", ProjectSlug: record.Slug})
	if err != nil {
		return workflow.InAdvanceQuestion{}, err
	}
	return inAdvanceQuestion(working, landing), nil
}

func inAdvanceQuestion(working, landing *adoption.ResolvedWorkflow) workflow.InAdvanceQuestion {
	lane := working.Name
	q := workflow.InAdvanceQuestion{Workflow: lane, Landing: []string{}}
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
		return q
	}
	if q.Acceptance, _ = accepts[0].With["inAdvance"].(string); q.Acceptance == "" {
		q.NotOffered = fmt.Sprintf("%s v%d's %s approval does not say what a yes given in advance means", lane, working.Version, accepts[0].Name)
		return q
	}
	for _, stage := range landing.Doc.Spec.Stages {
		for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
			if step.Uses != "approval" {
				continue
			}
			s, _ := step.With["inAdvance"].(string)
			if s == "" {
				q.NotOffered = fmt.Sprintf("the landing (%s v%d)'s %s approval does not say what a yes given in advance means", landing.Name, landing.Version, step.Name)
				return q
			}
			q.Landing = append(q.Landing, s)
		}
	}
	return q
}
