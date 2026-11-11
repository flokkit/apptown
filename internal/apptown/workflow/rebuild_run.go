package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
)

// StartRebuild starts a build-only system run for a release whose original
// images Deployment has confirmed missing. It cannot release or supply assent.
// The request identity makes redelivery reuse the same pinned run.
func (s *Service) StartRebuild(ctx context.Context, projectID, releaseID ids.ID, commit string, from int64) (ids.ID, error) {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Instrument != access.SystemInstrument {
		return ids.Nil, fmt.Errorf("recorded release rebuilds are started by AppTown")
	}
	if releaseID.IsZero() || from < 1 {
		return ids.Nil, fmt.Errorf("a rebuild needs a recorded release")
	}
	if s.starter == nil {
		return ids.Nil, fmt.Errorf("build execution is unavailable")
	}
	run, err := s.starter.start(ctx, StartRequest{ProjectID: projectID, WorkflowName: "rebuild", InputsJSON: "{}", sourceCommit: commit, rebuildReleaseID: releaseID, rebuildFrom: from, requestedBy: "apptown-system", receiptActor: "apptown-system"})
	if err != nil {
		return ids.Nil, err
	}
	return run.ID, nil
}
