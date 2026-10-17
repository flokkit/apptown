package forge

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// Policy interpretation belongs to Ways of Working; Work owns its gate and receipts.
type workflowLawRules struct{}

func (workflowLawRules) ViolationsIn(ctx context.Context, q database.Queryer, projectID ids.ID, spec *schema.Spec) ([]workflow.LawViolation, error) {
	policies, err := adoption.ReadProjectPolicies(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	evaluations, err := adoption.PrepareEvaluations(ctx, q, policies)
	if err != nil {
		return nil, err
	}
	violations := []workflow.LawViolation{}
	for _, p := range evaluations {
		if detail := p.Detail(spec); detail != "" {
			violations = append(violations, workflow.LawViolation{Policy: p.Name, Rule: p.Rule, Reading: p.Reading, Detail: detail})
		}
	}
	return violations, nil
}
