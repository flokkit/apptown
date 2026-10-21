package workflow

import (
	"context"

	"flokkit.io/forge/internal/database"
)

// CarriesNoCode reports whether a shipped change shipped no code of its own:
// only landing runs ever worked it (the ship workflow invoked on it to
// redeploy main) and none merged anything into main for it, so what it
// shipped was main as it stood. Only a shipped change is judged: one still
// open may yet land work a chat published to its branch, and a change whose
// landing merged has code. The caller still shows a branch that exists:
// commits on it that never reached main are the change's own.
func CarriesNoCode(ch *Change, roots []*Run) bool {
	if ch == nil || ch.State != ChangeShipped || ch.LandedSHA != "" || len(roots) == 0 {
		return false
	}
	for _, run := range roots {
		spec, err := run.Definition()
		if err != nil || spec.Role != "landing" {
			return false
		}
	}
	return true
}

// CarriesNoCodeIn is CarriesNoCode with the change's root runs read, for a
// caller that has authorized the change already.
func CarriesNoCodeIn(ctx context.Context, q database.Queryer, ch *Change) (bool, error) {
	if ch == nil || ch.State != ChangeShipped || ch.LandedSHA != "" {
		return false, nil
	}
	roots, err := readChangeRunsIn(ctx, q, ChangeRef{ID: ch.ID, ProjectID: ch.ProjectID}, ChangeRuns{RootsOnly: true})
	if err != nil {
		return false, err
	}
	return CarriesNoCode(ch, roots), nil
}
