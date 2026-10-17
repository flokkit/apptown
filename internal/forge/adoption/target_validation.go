package adoption

import (
	"context"
	"database/sql"
	"errors"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"slices"

	checkpkg "flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
)

// validateTargetChecksIn binds Work's acceptance to the exact guidance change,
// under the same project lock as target admission. Presentation summaries cannot
// remove checks from the Work gate.
func (s *Service) validateTargetChecksIn(ctx context.Context, q database.Queryer, projectID ids.ID, c Candidate) error {
	prior, err := CurrentReceiptIn(ctx, q, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		current, readErr := CurrentIn(ctx, q, projectID)
		if readErr != nil {
			return readErr
		}
		if current.Revision != 0 {
			return fmt.Errorf("project %d: revision %d: %w", projectID, current.Revision, ErrRevisionUnavailable)
		}
		prior = &RecordedRevision{ProjectID: projectID}
	} else if err != nil {
		return err
	}
	checks, err := (candidateBuilder{ways: s.deps.Ways}).newChecks(ctx, prior.Receipt, c.Receipt)
	if err != nil {
		return err
	}
	if !slices.Equal(checks, c.NewChecks) {
		return fmt.Errorf("target acceptance does not match the checks introduced by its guidance")
	}
	return nil
}

// newChecks preserves manifest order and authored check order. An unchanged
// publication needs no new acceptance; an added, re-enabled or changed one
// does, except a check no landing can judge (check.JudgedAtLanding).
func (a candidateBuilder) newChecks(ctx context.Context, prior, next Receipt) ([]CheckSummary, error) {
	have := make(map[string]ReceiptBlock, len(prior.Guidance))
	for _, b := range prior.Guidance {
		have[b.Name] = b
	}
	out := []CheckSummary{}
	for _, b := range next.Guidance {
		old, had := have[b.Name]
		if had && !(old.Disabled && !b.Disabled) && old.Version == b.Version {
			continue
		}
		checks, err := a.checksOfBlock(ctx, b.BlockID, b.Version)
		if err != nil {
			return nil, err
		}
		for _, check := range checks {
			// What no landing can judge is no uplift's acceptance: it is
			// judged in reviews once the project runs the new version.
			if !checkpkg.JudgedAtLanding(checkpkg.Declaration{Name: check.Name, Evaluator: check.Evaluator, Probe: check.Probe}) {
				continue
			}
			out = append(out, CheckSummary{Block: b.Name, Version: b.Version, Check: check.Name, Title: check.Title, Expects: check.Expects})
		}
	}
	return out, nil
}
