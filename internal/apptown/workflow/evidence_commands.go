package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// ArtifactDraft preserves original text or binary body bytes unchanged.
type ArtifactDraft struct{ Kind, Name, Ref, Body string }

// AttachArtifact commits the artifact and its receipt together, then refreshes
// dependent checks. A failed refresh cannot undo the committed artifact.
func (s *Service) AttachArtifact(ctx context.Context, runID ids.ID, in ArtifactDraft) (ids.ID, error) {
	project, _, err := s.runScope(ctx, runID, actionAttachEvidence)
	if err != nil {
		return ids.Nil, err
	}
	if in.Kind == "" {
		return ids.Nil, fmt.Errorf("artifact kind is required")
	}
	id, err := database.Transact(ctx, s.db, func(tx *database.Tx) (ids.ID, error) {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, project); err != nil {
			return ids.Nil, err
		}
		run, err := runRecord(q.RunForUpdate(ctx, runID))
		if err != nil {
			return ids.Nil, err
		}
		if run.ProjectID != project {
			return ids.Nil, ErrRunMoved
		}
		id, err := AddArtifactIn(ctx, tx, runID, in.Kind, in.Name, in.Ref, in.Body, s.now())
		if err != nil {
			return ids.Nil, err
		}
		payload, err := json.Marshal(struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			ID   ids.ID `json:"id"`
		}{in.Kind, in.Name, id})
		if err == nil {
			err = recordEventIn(ctx, tx, runID, "artifact.added", payload, s.now())
		}
		return id, err
	})
	// The artifact is committed: its evidence is refreshed though the
	// caller has gone.
	if err == nil {
		if follow := s.refreshAssertions(context.WithoutCancel(ctx), RunRef{ID: runID, ProjectID: project}); follow != nil {
			s.dispatcher.report(fmt.Errorf("run %d: refreshing attached evidence: %w", runID, follow))
		}
	}
	return id, err
}

// RefreshChangeEvidence is the after-commit handoff from an evidence owner such
// as ADRs. Work resolves the change's current run; transports do not read it.
func (s *Service) RefreshChangeEvidence(ctx context.Context, projectID, changeID ids.ID) error {
	if _, err := s.require(ctx, operationPermission{Action: actionRunStep, ProjectID: projectID, ChangeID: changeID}); err != nil {
		return err
	}
	change, err := readChangeInProject(ctx, s.db, changeID, projectID)
	if err != nil || change.RunID.IsZero() {
		return err
	}
	return s.refreshAssertions(ctx, RunRef{ID: change.RunID, ProjectID: projectID})
}

// AttachAgentReport stores what an agent says about its own work when it hands
// a change off, as an artifact of kind agentReportKind on the landing run. It
// needs the authority to request landing, not to attach evidence: it is a claim
// shown as reported, and no gate or requirement reads it. Past narrativeBudget
// bytes it is cut, and the stored text says so.
func (s *Service) AttachAgentReport(ctx context.Context, runID ids.ID, report string) error {
	project, _, err := s.runScope(ctx, runID, actionRequestLanding)
	if err != nil {
		return err
	}
	if len(report) > narrativeBudget {
		report = fmt.Sprintf("[first %d of %d bytes shown; the rest was cut]\n", narrativeBudget, len(report)) + strs.Truncate(report, narrativeBudget)
	}
	_, err = database.Transact(ctx, s.db, func(tx *database.Tx) (ids.ID, error) {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, project); err != nil {
			return ids.Nil, err
		}
		return AddArtifactIn(ctx, tx, runID, agentReportKind, "agent report", "", report, s.now())
	})
	return err
}
