package workcommands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// StandingProcedures reads current governed declarations. It does not infer a
// fallback workflow from failed selection or reuse accept-in-advance sentences.
type StandingProcedures struct {
	Adoptions    *adoption.Service
	Publications interface {
		PublishedWorkflow(context.Context, database.Queryer, string) (*adoption.ResolvedWorkflow, error)
	}
	Lanes *projectlifecycle.Service
	Laws  workflow.LawRules
}

func (s StandingProcedures) StandingProcedure(ctx context.Context, q database.Queryer, id ids.ID, name string) (workflow.StandingProcedure, error) {
	var out workflow.StandingProcedure
	if s.Lanes == nil || s.Laws == nil {
		return out, fault.New(fault.Unavailable, "standing workflow observation is unavailable")
	}
	p, err := project.ByIDIn(ctx, q, id)
	if err != nil {
		return out, err
	}
	working, err := s.Lanes.ResolveWorkflow(ctx, &p, name)
	if err != nil {
		return out, err
	}
	landing, err := s.Lanes.ResolveWorkflow(ctx, &p, "land")
	if err != nil {
		return out, err
	}
	for _, w := range []*adoption.ResolvedWorkflow{working, landing} {
		violations, err := s.Laws.ViolationsIn(ctx, q, id, &w.Doc.Spec)
		if err != nil {
			return out, err
		}
		if len(violations) > 0 {
			return out, fault.New(fault.Conflict, "the selected workflow does not satisfy current project law")
		}
	}
	out.Acceptance, err = standingPurpose(working, true)
	if err != nil {
		return out, err
	}
	out.Landing, err = standingPurpose(landing, false)
	return out, err
}
func standingPurpose(w *adoption.ResolvedWorkflow, working bool) (workflow.StandingPurpose, error) {
	var out workflow.StandingPurpose
	role := "landing"
	if working {
		role = "working"
	}
	if w.Doc.Spec.Role != role {
		return out, fault.New(fault.Conflict, "standing consent requires a workflow with the "+role+" role")
	}
	var approvals []schema.Step
	atFinal := false
	for i, stage := range w.Doc.Spec.Stages {
		for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
			if step.Uses == "approval" {
				approvals = append(approvals, step)
				atFinal = i == len(w.Doc.Spec.Stages)-1
			}
		}
	}
	if len(approvals) != 1 || (working && !atFinal) {
		return out, fault.New(fault.Conflict, "standing consent requires one explicit final working acceptance and one landing approval")
	}
	raw, err := json.Marshal(approvals[0].With)
	if err != nil {
		return out, err
	}
	cfg, err := workflow.ReadApprovalConfig(raw)
	if err != nil {
		return out, err
	}
	if cfg.Standing == nil {
		return out, fault.New(fault.Conflict, fmt.Sprintf("%s v%d / %s requires a person's decision for each change", w.Name, w.Version, approvals[0].Name))
	}
	return workflow.StandingPurpose{Workflow: w.Name, Version: int64(w.Version), Step: approvals[0].Name, Means: cfg.Standing.Means, Approvers: cfg.Approvers}, nil
}

// InstanceStandingProcedure pins the library declarations without selecting a
// project's adopted version. Eligibility later compares its adopted meanings.
func (s StandingProcedures) InstanceStandingProcedure(ctx context.Context, q database.Queryer, name string) (workflow.StandingProcedure, error) {
	var out workflow.StandingProcedure
	purposes := []*workflow.StandingPurpose{&out.Acceptance, &out.Landing}
	for i, n := range []string{name, "land"} {
		if s.Publications == nil {
			return out, fault.New(fault.Unavailable, "published standing procedures are unavailable")
		}
		resolved, err := s.Publications.PublishedWorkflow(ctx, q, n)
		if err != nil {
			return out, err
		}
		*purposes[i], err = standingPurpose(resolved, i == 0)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// StandingTargetProcedure reads the candidate's frozen procedures; publishing a
// new approval meaning cannot silently replace the one the person consented to.
func (s StandingProcedures) StandingTargetProcedure(ctx context.Context, q database.Queryer, pid, target ids.ID, name string) (workflow.StandingProcedure, error) {
	var out workflow.StandingProcedure
	ad, err := adoption.ProjectAdoptionIn(ctx, q, pid, target)
	if err != nil {
		return out, err
	}
	for i, n := range []string{name, "land"} {
		w, err := s.Adoptions.Workflow(ctx, adoption.WorkflowRequest{Source: adoption.Source{ProjectID: pid, Revision: ad}, Name: n})
		if err != nil {
			return out, err
		}
		violations, err := s.Laws.ViolationsIn(ctx, q, pid, &w.Doc.Spec)
		if err != nil {
			return out, err
		}
		if len(violations) > 0 {
			return out, fault.New(fault.Conflict, "the target workflow does not satisfy current project law")
		}
		purpose, err := standingPurpose(w, i == 0)
		if err != nil {
			return out, err
		}
		if i == 0 {
			out.Acceptance = purpose
		} else {
			out.Landing = purpose
		}
	}
	return out, nil
}
