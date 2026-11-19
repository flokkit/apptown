package verification

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
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

func (r routineInspector) inspectUpdates(ctx context.Context, source workflow.Source, categories workflow.JanitorChoices) (out workflow.StandingInspection, err error) {
	out.Classifier = "agent-update-v1"
	out.Classification = "cannot-assess"
	before, err := r.observe(ctx, source)
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
	var readings []imagelines.Reading
	if err := CompareUpdateInputs(ctx, inputs[0], inputs[1], categories.BaseImages, ImageUpdates{Resolver: r.images, Scope: UpdateScope(categories.Dependencies), Security: len(targets) > 0, ToolchainPatches: categories.ToolchainPatches, Readings: &readings}); err != nil {
		if fault.KindOf(err) == fault.Unavailable {
			out.Reason = err.Error()
			return out, nil
		}
		return refuse(err.Error())
	}
	if len(targets) > 0 && !categories.SecurityFixes {
		return refuse("security fixes are not permitted by this rule")
	}
	after, err := r.observe(ctx, source)
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
	out.Reason = "toolchain and base-image inputs stay within the permitted bounds; the routine model receipt and required checks govern standing acceptance"
	var evidence []string
	for _, r := range readings {
		if r.Stale {
			evidence = append(evidence, r.Evidence())
		}
	}
	if len(evidence) > 0 {
		out.Reason += "; " + strings.Join(evidence, "; ")
	}
	return out, nil
}
