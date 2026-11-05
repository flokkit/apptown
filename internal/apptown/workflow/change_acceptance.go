package workflow

import (
	"context"
	"slices"

	"github.com/flokkit/apptown/internal/ids"
)

// changeAcceptance identifies a source change and its recorded accepting actors.
// Author/requester identity is deliberately absent: it cannot imply assent.
type changeAcceptance struct {
	ChangeID   ids.ID   `json:"changeId"`
	AcceptedBy []string `json:"acceptedBy"`
}

// AcceptanceByNumber reads only within the given project and under the caller's
// normal change/run/evidence read authority. Missing or unreadable records fail.
func (s *Service) AcceptanceByNumber(ctx context.Context, projectID ids.ID, slug string, number int64) (changeAcceptance, error) {
	out := changeAcceptance{AcceptedBy: []string{}}
	if _, err := s.require(ctx, operationPermission{Action: actionListChanges, ProjectID: projectID}); err != nil {
		return out, err
	}
	id, err := ChangeIDByNumberIn(ctx, s.db, projectID, slug, number)
	if err != nil {
		return out, err
	}
	if _, err := s.Change(ctx, id); err != nil {
		return out, err
	}
	out.ChangeID = id
	runs, err := s.ChangeRuns(ctx, id, ChangeRuns{RootsOnly: true})
	if err != nil {
		return out, err
	}
	for _, run := range runs {
		snapshot, err := s.Snapshot(ctx, run.ID, DetailView)
		if err != nil {
			return out, err
		}
		for _, step := range snapshot.Steps {
			if step.Type != "approval" || step.Status != StatusPass {
				continue
			}
			ev, err := ReadApprovalEvidence(step.Evidence)
			if err != nil {
				return out, err
			}
			if ev.Withdrawal != nil || ev.EvalAuthorised != "" {
				continue
			}
			for _, vote := range ev.Decisions {
				if vote.Decision == VoteApprove && !slices.Contains(out.AcceptedBy, vote.Approver) {
					out.AcceptedBy = append(out.AcceptedBy, vote.Approver)
				}
			}
		}
	}
	return out, nil
}
