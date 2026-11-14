package workcommands

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
)

// PackAdoptions turns finished assessments into governed Changes. A failed
// assessment is still a useful candidate: checks and repair judge the Change.
type PackAdoptions struct {
	Adoption *adoption.Service
	Work     *workflow.Service
	Rules    *workflow.StandingRules
}

func (a PackAdoptions) AdoptWhatIf(ctx context.Context, assessed *conformance.WhatIf) error {
	if assessed.State != conformance.WhatIfDone || assessed.RuleID.IsZero() {
		return nil
	}
	for _, p := range assessed.Projects {
		if p.CandidateDigest == "" {
			continue
		}
		call := access.WithCaller(ctx, access.Caller{Verified: true, Receipt: "apptown-system", Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: p.ProjectID}})
		call = adoption.WithSystemAuthority(call, p.ProjectID)
		picks, err := a.Adoption.PicksWith(call, p.ProjectID, assessed.Pack, assessed.Version, verification.AlsoPicks(assessed.Also)...)
		if err != nil {
			return err
		}
		candidate, err := a.Adoption.PrepareAssessed(call, p.ProjectID, adoption.Request{Packs: picks, AdvanceWorkflows: assessed.AdvanceWorkflows}, p.AdoptionRevision, p.CandidateDigest)
		if fault.KindOf(err) == fault.Conflict {
			continue
		}
		if err != nil {
			return err
		}
		if len(candidate.Problems) > 0 {
			continue
		}

		procedure, queue, err := adoptionProcedure(call, a.Adoption, *candidate)
		if err != nil {
			return err
		}
		name := procedure.Name
		rule, _, _, err := a.Rules.StandingForWorkflow(ctx, p.ProjectID, name)
		if err != nil {
			return err
		}
		if rule.RuleID.IsZero() || !rule.Question.Definition.Categories.WaysOfWorking {
			continue
		}
		fingerprint, err := candidate.Fingerprint()
		if err != nil {
			return err
		}
		state, err := a.Work.QueueState(call, p.ProjectID, queue)
		if err != nil {
			return err
		}
		if !state.ChangeID.IsZero() {
			tries, err := a.Work.Attempts(call, p.ProjectID, queue, 1)
			if err != nil {
				return err
			}
			if len(tries) > 0 && tries[0].Fingerprint == fix.ProposalFingerprint(p.SHA, fingerprint) {
				continue
			}
		}
		ch, err := a.Work.OpenPackUplift(call, workflow.Uplift{ProjectID: p.ProjectID, ExpectedRevision: candidate.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: candidate.Digest, Checks: workAcceptanceChecks(candidate.NewChecks)}, Words: candidate.Words(), Target: upliftTarget{service: a.Adoption, candidate: *candidate}, Basis: fmt.Sprintf("Assessed in what-if %s at %s.", assessed.ID, p.SHA), RuleID: rule.RuleID, RuleRevision: rule.Revision, Workflow: name, Queue: queue})
		if fault.KindOf(err) == fault.Conflict {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := a.Work.StartRun(call, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: name}); err != nil {
			return err
		}
	}
	return nil
}
