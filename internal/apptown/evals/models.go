package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// errModelOutsideArm is a model call for an eval copy with a model its arm
// does not run: refused before it is sent, and read as "could not assess".
var errModelOutsideArm = fault.New(fault.Unprocessable, "could not assess: the model is not one this eval arm runs")

// AdmitModel is the one model choice, and the spend gate, at every model
// dispatch for a project: AppTown's own calls (checks, narratives,
// briefs) and an agent's through the inference route alike. For an eval
// copy the call is sent only with a model its arm runs, as its slots named
// when the eval started, and only while its eval may proceed: a stopping,
// deleted or ended eval, or a spent budget, refuses it unsent ("could not
// assess"; a step waits). Unknown spend holds new arms, not the calls of an
// arm already running. Any project that is not a copy is admitted
// unchanged. A read failure is an error, never a yes.
func (g *Gate) AdmitModel(ctx context.Context, projectID ids.ID, pattern string) error {
	allowed, copy, err := armModelsIn(ctx, g.db, projectID)
	if err != nil || !copy {
		return err
	}
	if !slices.Contains(allowed, pattern) {
		return fmt.Errorf("%w: %s (it runs %v)", errModelOutsideArm, pattern, allowed)
	}
	if err := g.admit(ctx, projectID); err != nil {
		return fmt.Errorf("could not assess: %w", err)
	}
	return nil
}

// errNotTheJudge is a call through the judge's door that is not the
// judging of an eval copy by its recorded judge.
var errNotTheJudge = fault.New(fault.Unprocessable, "could not assess: the judge's door admits only an eval's recorded judge for its copies")

// AdmitJudge is the model choice of the judge's door: the one door an eval's comparison sends its rubric calls through. It admits only
// the judge recorded for the copy's Change at start (the baseline's fast
// model, the same for both arms, so the judge is not what varies), only
// for a copy, and only while the eval may proceed; the spend is the
// copy's, inside the eval's budget. The arm's own models do not grow:
// nothing a copy's agents or checks reach goes through this door, so a
// variant agent still cannot use a baseline-only model.
func (g *Gate) AdmitJudge(ctx context.Context, projectID ids.ID, pattern string) error {
	_, copy, err := copyOfIn(ctx, g.db, projectID)
	if err != nil {
		return err
	}
	if !copy {
		return fmt.Errorf("%w: project %d is not an eval copy", errNotTheJudge, projectID)
	}
	plan, err := planOfCopyIn(ctx, g.db, projectID)
	if err != nil {
		return err
	}
	if plan.Judge == "" || plan.Judge != pattern {
		return fmt.Errorf("%w: %s (the judge is %q)", errNotTheJudge, pattern, plan.Judge)
	}
	if err := g.admit(ctx, projectID); err != nil {
		return fmt.Errorf("could not assess: %w", err)
	}
	return nil
}

// planOfCopyIn is the plan of the arm a copy was made for.
func planOfCopyIn(ctx context.Context, q database.Queryer, projectID ids.ID) (armPlan, error) {
	raw, err := queries(q).ModelsOfCopy(ctx, projectID)
	if err != nil {
		// A copy whose arm cannot be read is refused, never let through.
		return armPlan{}, fmt.Errorf("reading project %d's eval arm: %w", projectID, err)
	}
	var plan armPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return armPlan{}, fmt.Errorf("reading project %d's eval arm: %w", projectID, err)
	}
	return plan, nil
}

// armModelsIn is the distinct models the copy's arm runs; copy is false for
// a project that is not a copy.
func armModelsIn(ctx context.Context, q database.Queryer, projectID ids.ID) (models []string, copy bool, err error) {
	if _, copy, err := copyOfIn(ctx, q, projectID); err != nil || !copy {
		return nil, false, err
	}
	plan, err := planOfCopyIn(ctx, q, projectID)
	if err != nil {
		return nil, true, err
	}
	for _, pattern := range plan.Slots {
		if pattern != "" && !slices.Contains(models, pattern) {
			models = append(models, pattern)
		}
	}
	sort.Strings(models)
	return models, true, nil
}
