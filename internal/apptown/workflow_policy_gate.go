package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
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
