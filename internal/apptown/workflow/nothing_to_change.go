package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// A step may declare that an empty result is a complete answer
// (`mayChangeNothing`, a maintenance lane with nothing to update). When it
// leaves the change empty, its evidence says nothingToChange, and the run
// ends instead of asking anyone to accept an empty change, once all of these
// hold at the closing act (endNothingToChange):
//
//   - the change is still empty against its base, read under the
//     repository's lock (a later step in the stage may have changed it);
//   - an independent review of that step's report passed, and no review in
//     the stage left a finding: a deferred major or a reverted update is a
//     finding, and leaves the decision to a person, whichever review
//     raised it;
//   - every other step of the stage has settled as passed or skipped, no
//     required check is missing, and only pending approvals hold the gate.
//
// Anything else (a failed scan, a running review, a rejected approval)
// leaves the run waiting for a person as before.

// NothingToChange is the account of the step that found nothing to change,
// when the stage meets the conditions above apart from the change's
// emptiness, which only the closing act can read.
func (a Advancement) NothingToChange(steps []*Step) (string, bool) {
	if a.Passed() || len(a.Missing) > 0 {
		return "", false
	}
	for _, s := range a.Blocked {
		if s.Type != "approval" || s.Status != StatusPending {
			return "", false
		}
	}
	var account, kind string
	for _, s := range steps {
		switch {
		case s.Type == "approval" && s.Status == StatusPending:
		case !s.Status.Judged() && s.Status != StatusSkipped:
			return "", false
		}
		var ev struct {
			NothingToChange bool   `json:"nothingToChange"`
			Reason          string `json:"reason"`
		}
		var cfg struct {
			Output struct {
				Kind string `json:"kind"`
			} `json:"output"`
		}
		if s.Status == StatusPass && json.Unmarshal(s.Evidence, &ev) == nil && ev.NothingToChange &&
			json.Unmarshal(s.Config, &cfg) == nil && cfg.Output.Kind != "" {
			account, kind = ev.Reason, cfg.Output.Kind
		}
	}
	if kind == "" || !reviewedWithoutFindings(steps, kind) {
		return "", false
	}
	return account, true
}

// reviewedWithoutFindings: an agent/review of the report kind passed, and
// no review of the stage left anything for a person. A review whose
// evidence cannot be read has not said it left nothing.
func reviewedWithoutFindings(steps []*Step, kind string) bool {
	reviewed := false
	for _, s := range steps {
		if s.Type != "agent/review" {
			continue
		}
		var cfg struct {
			Of string `json:"of"`
		}
		var ev struct {
			Findings []string `json:"findings"`
		}
		if json.Unmarshal(s.Evidence, &ev) != nil || len(ev.Findings) > 0 {
			return false
		}
		if s.Status == StatusPass && json.Unmarshal(s.Config, &cfg) == nil && cfg.Of == kind {
			reviewed = true
		}
	}
	return reviewed
}

// ChangeIsEmpty: the change's published branch changes nothing against its
// base (scm.ChangeBase), or was never published. Off a change, or without a
// repository, there is no change to be empty.
func ChangeIsEmpty(ctx context.Context, dataDir string, source Source) (bool, error) {
	if !source.RepoBacked() || source.ChangeID.IsZero() || source.RootRole == "landing" {
		return false, nil
	}
	hub := source.Repository(dataDir)
	if tip, err := gitexec.HubReadRef(ctx, hub, "refs/heads/"+source.Branch); err != nil || tip == "" {
		return tip == "" && err == nil, err
	}
	diff, err := scm.ReadChangeDiff(ctx, hub, hub, source.DefaultBranch, source.ChangeBaseSHA, source.Branch)
	if err != nil {
		return false, err
	}
	return diff.Empty, nil
}

// endNothingToChange closes a change whose run found nothing to change: the
// AppTown closes it with the step's account as the reason; nothing is landed
// and no approval is asked. Every condition is read again at the act: the
// change's emptiness under the repository's lock (no Git runs in the
// transaction), then the run, its stage and its steps in the transaction
// that closes it. A run that moved, or a change that is no longer empty,
// is ErrRunMoved and stays open; a read failure is an error, never empty.
func (s *Service) endNothingToChange(ctx context.Context, expected *Run) error {
	closure, err := scm.PrepareClosure(ctx, s.abandonments.locks, expected.ProjectID)
	if err != nil {
		return err
	}
	defer closure.Close()
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: expected.ID, ProjectID: expected.ProjectID})
	if err != nil {
		return err
	}
	switch empty, err := ChangeIsEmpty(ctx, s.dataDir, source); {
	case err != nil:
		return err
	case !empty:
		return ErrRunMoved
	}
	_, err = database.Transact(ctx, s.db, func(tx *database.Tx) (*Abandoned, error) {
		run, err := lockCurrentRunIn(ctx, tx, expected)
		if err != nil {
			return nil, err
		}
		steps, err := readStageSteps(ctx, tx, run.ID, run.CurrentStage)
		if err != nil {
			return nil, err
		}
		checks, err := readStageChecks(ctx, tx, run.ID, run.CurrentStage)
		if err != nil {
			return nil, err
		}
		decision, err := run.Advance(steps, checks)
		if err != nil {
			return nil, err
		}
		account, ok := decision.NothingToChange(steps)
		if !ok || run.ChangeID != expected.ChangeID || !run.ParentRunID.IsZero() {
			return nil, ErrRunMoved
		}
		if account = strings.TrimSpace(account); account == "" {
			account = "nothing to change"
		}
		reason := strings.ToUpper(account[:1]) + account[1:]
		return s.abandonments.abandonIn(ctx, tx, Abandonment{ChangeID: run.ChangeID, Who: "apptown-system", Reason: reason, closedAs: ClosedNothingToChange}, run.ProjectID, closure)
	})
	return err
}
