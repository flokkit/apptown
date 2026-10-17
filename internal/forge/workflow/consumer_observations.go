package workflow

import (
	"context"
	"database/sql"
	"fmt"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type ChangeBranch struct {
	ID     ids.ID
	Branch string
}

// ShippedBranchesIn supplies already-admitted conformance attribution. Git owns
// ancestry; Work owns which Changes are shipped and which branch each recorded.
func ShippedBranchesIn(ctx context.Context, q database.Queryer, project ids.ID) ([]ChangeBranch, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("shipped branches require project scope")
	}
	rows, err := workflowsql.New(q.SQL()).ShippedChangeBranchesMeta(ctx, workflowsql.ShippedChangeBranchesMetaParams{ProjectID: project, State: ChangeShipped.String()})
	if err != nil {
		return nil, database.Missing(err)
	}
	out := make([]ChangeBranch, 0, len(rows))
	for _, row := range rows {
		out = append(out, ChangeBranch{ID: row.ID, Branch: row.Branch})
	}
	return out, nil
}

// LandingApproverIn reads accepted landing evidence for this Change and
// project: who closed the landing's approval, with the principal and way
// their vote recorded (never their name looked up again). Missing evidence
// is an empty attribution; a failed read is an error.
func LandingApproverIn(ctx context.Context, q database.Queryer, change ChangeRef) (access.Actor, error) {
	if change.ID.IsZero() || change.ProjectID.IsZero() {
		return access.Actor{}, fmt.Errorf("landing attribution requires Change scope")
	}
	values, err := workflowsql.New(q.SQL()).LandingApprover(ctx, workflowsql.LandingApproverParams{ProjectID: change.ProjectID, ChangeID: change.ID, PassState: StatusPass.String()})
	if err != nil || len(values) == 0 {
		return access.Actor{}, err
	}
	evidence, err := ReadApprovalEvidence(values[0])
	if err != nil {
		return access.Actor{}, err
	}
	who := access.Actor{Name: evidence.Approver}
	for _, vote := range evidence.Decisions {
		if vote.Approver == evidence.Approver && vote.Decision == evidence.Decision {
			who.Principal, who.Via = vote.Principal, vote.Via
		}
	}
	return who, nil
}

// ChatContinuation is a scoped observation of the stopped-at Step, its Run and
// the last producing agent in that stage. It never authorizes abandonment.
type ChatContinuation struct {
	StepContext
	Producer *Step
	Source   Source
	// History is what the producer's latest attempt kept of its session: a
	// chat resumes those bytes, never the folder's newest file.
	History TranscriptReceipt
}

func (s *Service) ChatContinuation(ctx context.Context, changeID, stepID ids.ID) (ChatContinuation, error) {
	project, _, err := s.changeScope(ctx, changeID, actionReadEvidence)
	if err != nil {
		return ChatContinuation{}, err
	}
	ref, err := s.stepScope(ctx, stepID)
	if err != nil {
		return ChatContinuation{}, err
	}
	if ref.ProjectID != project {
		return ChatContinuation{}, sql.ErrNoRows
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return ChatContinuation{}, err
	}
	defer tx.Rollback()
	observed, err := readStepContext(ctx, tx, ref, stepID)
	if err != nil {
		return ChatContinuation{}, err
	}
	if observed.Run.ChangeID != changeID {
		return ChatContinuation{}, sql.ErrNoRows
	}
	out := ChatContinuation{StepContext: observed}
	steps, err := ReadScopedStepsIn(ctx, tx, ref)
	if err != nil {
		return ChatContinuation{}, err
	}
	for _, step := range steps {
		if step.Stage == observed.Step.Stage && step.Type == "agent/pi" {
			out.Producer = step
		}
	}
	if out.Producer != nil {
		out.Source, err = ReadSourceIn(ctx, tx, ref)
		if err != nil {
			return ChatContinuation{}, err
		}
		out.History, err = StepTranscriptIn(ctx, tx, ref, out.Producer.ID, 0)
		if err != nil {
			return ChatContinuation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ChatContinuation{}, err
	}
	return out, nil
}
