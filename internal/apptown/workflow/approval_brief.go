package workflow

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// ApprovalBriefInputs is one evidence snapshot for a human briefing. It is not
// an approval command; cached prose never authorizes a decision.
type ApprovalBriefInputs struct {
	StepContext
	Steps  []*Step
	Change *Change
	Report string
	// Asked is what a person last asked of the run's work since the change
	// was opened (a send-back note or an approver's rejection comment); nil
	// when nobody has. It is part of the ask, not a deviation from it.
	Asked     *PersonFeedback
	Cached    string
	CacheName string
	Acts      []NextAct
}

func (s *Service) ApprovalBrief(ctx context.Context, id ids.ID) (ApprovalBriefInputs, error) {
	ref, err := s.stepScope(ctx, id)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readBriefIn(ctx, tx, ref, id, false)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalBriefInputs{}, err
	}
	return out, nil
}

// readBriefIn composes observations in the supplied read snapshot.
func readBriefIn(ctx context.Context, tx *database.Tx, ref RunRef, id ids.ID, failure bool) (ApprovalBriefInputs, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() || id.IsZero() {
		return ApprovalBriefInputs{}, fmt.Errorf("approval brief requires Step and Run scope")
	}
	observed, err := readStepContext(ctx, tx, ref, id)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	if !failure && observed.Step.Type != "approval" {
		// Only an approval step has a brief.
		return ApprovalBriefInputs{}, database.Missing(sql.ErrNoRows)
	}
	out := ApprovalBriefInputs{StepContext: observed}
	steps, err := ReadScopedStepsIn(ctx, tx, ref)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	var latest stamp.Time
	for _, step := range steps {
		if step.Type != "approval" && step.FinishedAt.Valid && (!latest.Valid || step.FinishedAt.After(latest)) {
			latest = step.FinishedAt
		}
		// The current stage's steps, and every step that finished in an
		// earlier one: a landing's approval stands on the gates its earlier
		// stages passed, and without them the brief would say "no machine
		// checks passed".
		// A later stage's steps have not run and are not shown.
		if step.Stage == observed.Run.CurrentStage || step.FinishedAt.Valid {
			out.Steps = append(out.Steps, step)
		}
	}
	kind, name := "approval-brief", fmt.Sprintf("step-%d-v3", id)
	if failure {
		kind, name = "failure-advice", failureAdviceName(observed.Step)
	}
	out.CacheName = name
	cache, err := ReadRunArtifactsIn(ctx, tx, ref, ArtifactSelection{Kinds: []string{kind}, Name: name, Latest: true})
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	if len(cache) > 0 && cache[0].Body != "" && (failure || !latest.Valid || stamp.Parse(cache[0].CreatedAt).After(latest)) {
		out.Cached = cache[0].Body
		if !failure {
			return out, nil
		}
	}
	if !observed.Run.ChangeID.IsZero() {
		out.Change, err = readChangeInProject(ctx, tx, observed.Run.ChangeID, ref.ProjectID)
		if err != nil {
			return ApprovalBriefInputs{}, err
		}
	}
	claim, err := ReadClaimIn(ctx, tx, ref, observed.Run.ChangeID)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	if claim != nil {
		out.Report = claim.Body
	}
	out.Asked, err = LatestPersonFeedbackIn(ctx, tx, ref)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	return out, nil
}

// failureAdviceName identifies prose about one recorded attempt.
func failureAdviceName(step *Step) string {
	return fmt.Sprintf("step-%d-attempt-%d", step.ID, step.Attempt)
}

// FailureAdvice reads only a currently held check and its scoped evidence.
func (s *Service) FailureAdvice(ctx context.Context, id ids.ID) (ApprovalBriefInputs, error) {
	ref, err := s.stepScope(ctx, id)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := readWorkflowSnapshot(ctx, tx, ref.ID, DetailView, s.wait, ref.ProjectID, s.stepAttempts.automatic)
	if err != nil {
		return ApprovalBriefInputs{}, err
	}
	w := waitingOn(nil, d)
	if w.Kind != "check" || w.Step != id {
		return ApprovalBriefInputs{}, database.Missing(sql.ErrNoRows)
	}
	out, err := readBriefIn(ctx, tx, ref, id, true)
	if err != nil {
		return out, err
	}
	for _, act := range d.Next {
		if (act.Act != nextOverride && act.Act != nextRetry) || act.StepID == id {
			out.Acts = append(out.Acts, act)
		}
	}
	if out.Change != nil {
		out.Acts = append(out.Acts, ChangeActs(out.Change, d, false, false, false)...)
	}
	if err := tx.Commit(); err != nil {
		return ApprovalBriefInputs{}, err
	}
	return out, nil
}

// SaveBrief persists completed prose through workflow's artifact owner.
func (s *Service) SaveBrief(ctx context.Context, run ids.ID, kind, name, body string) error {
	_, err := AddArtifactIn(ctx, s.db, run, kind, name, "", body, s.Now())
	return err
}
