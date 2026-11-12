package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/routinechange"
)

// WithFixes binds classification to the run's exact adopted guidance. Missing
// declarations are an error, never a request to infer an ecosystem from files.
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
		targets, err := fix.Targets(run.InputsJSON)
		return ds, targets, err
	}
	return r
}

func (r routineInspector) inspectUpdates(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (out workflow.StandingInspection, err error) {
	out.Classifier = "declared-update-v1"
	out.Classification = "cannot-assess"
	before, err := r.observe(ctx, source, categories.BaseImages)
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
	var lists [2][]UpdateListing
	for i, commit := range []string{before.base, before.tip} {
		tree, remove, err := StageCommittedTree(ctx, r.dataDir, repo, commit)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		lists[i], err = ListUpdates(ctx, r.db, r.executor, tree, ds)
		remove()
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
	}
	refuse := func(reason string) (workflow.StandingInspection, error) {
		out.Classification = "requires-person"
		out.Reason = reason
		return out, nil
	}
	if len(lists[0]) != len(lists[1]) {
		return refuse("the declared parts changed")
	}
	bound := categories.Dependencies
	if bound == "" && categories.SecurityFixes {
		bound = "minor"
	}
	var beforePackages, afterPackages []fix.Listing
	for i, b := range lists[0] {
		a := lists[1][i]
		if b.Part != a.Part || b.Declaration != a.Declaration {
			return refuse("the declared parts changed")
		}
		if _, err := fix.CompareUpdate(b.Packages, a.Packages, bound, nil); err != nil {
			return refuse(fmt.Sprintf("part %s: %v", b.Part, err))
		}
		beforePackages = append(beforePackages, b.Packages...)
		afterPackages = append(afterPackages, a.Packages...)
	}
	if _, err := fix.CompareUpdate(beforePackages, afterPackages, bound, targets); err != nil {
		return refuse(err.Error())
	}
	if before.image && categories.BaseImages {
		candidate, err := routinechange.CompareImage(before.dockerBefore, before.dockerAfter)
		if err != nil {
			return refuse(err.Error())
		}
		if err := eligibleImageContract(before.contractBefore, before.contractAfter); err != nil {
			return refuse(err.Error())
		}
		evidence, err := r.validateImage(ctx, candidate)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		out.Images = evidence
	}
	after, err := r.observe(ctx, source, categories.BaseImages)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if !reflect.DeepEqual(before, after) {
		out.Reason = "source moved while dependency validation ran"
		return out, nil
	}
	raw, err := json.Marshal(lists)
	if err != nil {
		return out, err
	}
	out.Validation = &workflow.StandingValidation{Image: r.executor.AgentImage, ReportDigest: observationDigest(raw), ScratchSettlement: "local staging removed; executor scratch cleanup is not observed here"}
	out.Classification = "routine-dependency"
	if before.image && categories.BaseImages {
		out.Classification = "routine-dependency-and-image"
	}
	out.Reason = "the declared update is within the selected bound; classification does not accept, land or deploy it"
	return out, nil
}
