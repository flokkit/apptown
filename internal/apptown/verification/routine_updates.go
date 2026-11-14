package verification

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
)

// WithFixes binds classification to the run's exact adopted guidance. Missing
// declarations do not restrict which ecosystems the agent may update.
func (r routineInspector) WithFixes(db *database.DB, ad *adoption.Service) routineInspector {
	r.db = db
	r.fixes = func(ctx context.Context, source workflow.Source) ([]fix.Declaration, []check.Finding, error) {
		run, err := workflow.ReadRunIn(ctx, db, source.RunID)
		if err != nil {
			return nil, nil, err
		}
		revision, err := adoption.ByIDIn(ctx, db, run.AdoptionID)
		if err != nil {
			return nil, nil, err
		}
		ds, err := ad.Fixes(ctx, adoption.Source{ProjectID: source.ProjectID, Revision: revision})
		if err != nil {
			return nil, nil, err
		}
		targets, err := source.UpdateTargetsIn(ctx, db)
		return ds, targets, err
	}
	return r
}

func (r routineInspector) inspectUpdates(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (out workflow.StandingInspection, err error) {
	out.Classifier = "agent-update-v1"
	out.Classification = "cannot-assess"
	before, err := r.observe(ctx, source, false)
	out.Base, out.Candidate, out.Tree = before.base, before.tip, before.tree
	for _, f := range before.files {
		out.ChangedFiles = append(out.ChangedFiles, f.Path)
	}
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if len(before.files) == 0 {
		out.Classification = "not-applicable"
		out.Reason = "nothing to change"
		return out, nil
	}
	if r.fixes == nil || r.db == nil {
		out.Reason = "adopted fix declarations are unavailable"
		return out, nil
	}
	ds, targets, err := r.fixes(ctx, source)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	repo, err := source.Repository(r.dataDir)
	if err != nil {
		return out, err
	}
	var inputs [2]map[string]any
	for i, commit := range []string{before.base, before.tip} {
		tree, remove, err := StageCommittedTree(ctx, r.dataDir, repo, commit)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		inputs[i], err = UpdateInputs(tree)
		if err != nil {
			remove()
			out.Reason = err.Error()
			return out, nil
		}
		remove()
	}
	refuse := func(reason string) (workflow.StandingInspection, error) {
		out.Classification = "requires-person"
		out.Reason = reason
		return out, nil
	}
	if err := CompareUpdateInputs(inputs[0], inputs[1]); err != nil {
		return refuse(err.Error())
	}
	if len(targets) > 0 && !categories.SecurityFixes {
		return refuse("security fixes are not permitted by this rule")
	}
	after, err := r.observe(ctx, source, false)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if !reflect.DeepEqual(before, after) {
		out.Reason = "source moved while dependency validation ran"
		return out, nil
	}
	raw, err := json.Marshal(fix.Classify(out.ChangedFiles, ds, inputs[:]...))
	if err != nil {
		return out, err
	}
	out.Validation = &workflow.StandingValidation{Image: r.executor.AgentImage, ReportDigest: observationDigest(raw), ScratchSettlement: "local staging removed; executor scratch cleanup is not observed here"}
	out.Classification = "routine-dependency"
	out.Reason = "toolchain versions and base-image inputs are unchanged; the routine model receipt and required checks govern standing acceptance"
	return out, nil
}
