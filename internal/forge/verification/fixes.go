package verification

// Fixes as records (design/subsystems/remediation.md §9.1): when a review shows a
// project's verdict go from failing to conforming, that is a FIX, and the
// project keeps it — what was fixed, by which Change(s), verified by which
// review, accepted by whom. Written by the review, not by a job, so a fix
// made by hand or by an ordinary feature change is recorded the same way.
// Attribution is a git question with a recorded answer: the landed Changes
// whose branches lie in before.sha..after.sha on the default branch.

import (
	"context"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
)

// Prepare attribution facts outside the admission transaction. A failed read
// stops the review; it cannot masquerade as an unattributed or unconfirmed fix.
func (f Fleet) prepareVerdictFix(ctx context.Context, prev, next conformance.Verdict) (*conformance.FixAttribution, error) {
	changes, commits, err := f.changesLandedBetween(ctx, next.ProjectID, prev.SHA, next.SHA)
	if err != nil {
		return nil, err
	}
	attribution := &conformance.FixAttribution{Changes: changes, Commits: commits}
	if len(changes) == 1 {
		item, _, err := remediation.ItemForChangeIn(ctx, f.db, changes[0])
		if err != nil {
			return nil, err
		}
		attribution.RemediationItem = item
		attribution.AcceptedBy, err = workflow.LandingApproverIn(ctx, f.db, workflow.ChangeRef{ID: changes[0], ProjectID: next.ProjectID})
		if err != nil {
			return nil, err
		}
	}
	return attribution, nil
}

// changesLandedBetween attributes a flip to the shipped Changes whose branch
// tips are ancestors of `to` but not of `from` on the project repo. Empty
// when none can be named; commits then carries the range for the record.
func (f Fleet) changesLandedBetween(ctx context.Context, projectID ids.ID, from, to string) ([]ids.ID, string, error) {
	slugMeta, slugErr := project.ByIDIn(ctx, f.db, projectID)
	slug := slugMeta.Slug
	if slugErr != nil {
		return nil, "", slugErr
	}
	repo := filepath.Join(f.dataDir, "workspaces", "project-"+slug)
	if !gitexec.IsRepo(repo) || from == "" || to == "" || from == to {
		return nil, "", nil
	}
	cs, err := workflow.ShippedBranchesIn(ctx, f.db, projectID)
	if err != nil {
		return nil, "", err
	}
	var idList []ids.ID
	for _, c := range cs {
		tip, err := gitexec.Run(ctx, repo, "rev-parse", "--verify", "--quiet", c.Branch)
		if err != nil || strings.TrimSpace(tip) == "" {
			continue
		}
		tip = strings.TrimSpace(tip)
		if _, err := gitexec.Run(ctx, repo, "merge-base", "--is-ancestor", tip, to); err != nil {
			continue // not landed by `to`
		}
		if _, err := gitexec.Run(ctx, repo, "merge-base", "--is-ancestor", tip, from); err == nil {
			continue // already landed by `from`
		}
		idList = append(idList, c.ID)
	}
	commits := ""
	if len(idList) == 0 {
		commits = strs.ShortSHA(from) + ".." + strs.ShortSHA(to)
	}
	return idList, commits, nil
}
