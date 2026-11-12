package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// AutomatedAttempt is one compact record of an automated Change, kept from admission.
// OpenChange is empty after disposal; Observations retain exact subjects.
type AutomatedAttempt struct {
	Fingerprint  string                        `json:"fingerprint"`
	Packages     json.RawMessage               `json:"packages"`
	ID           ids.ID                        `json:"id"`
	ProjectID    ids.ID                        `json:"projectId"`
	OpenChange   ids.ID                        `json:"openChange,omitzero"`
	Queue        string                        `json:"queue"`
	What         string                        `json:"what"`
	When         time.Time                     `json:"when"`
	Outcome      string                        `json:"outcome"`
	Reason       string                        `json:"reason"`
	Observations []conformance.ScanObservation `json:"observations"`
}

func (s *Service) Attempts(ctx context.Context, project ids.ID, queue string, limit int) ([]AutomatedAttempt, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := workflowsql.New(s.db.SQL()).AutomatedChangeRecords(ctx, workflowsql.AutomatedChangeRecordsParams{ProjectID: project, Column2: queue, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	observations, err := conformance.ScanObservationsIn(ctx, s.db, project)
	if err != nil {
		return nil, err
	}
	out := make([]AutomatedAttempt, 0, len(rows))
	for _, row := range rows {
		a := AutomatedAttempt{Fingerprint: row.Fingerprint, Packages: row.Packages, ID: row.ID, ProjectID: project, Queue: row.Queue, What: row.What, When: row.OpenedAt, Outcome: row.Outcome, Reason: row.Reason, Observations: []conformance.ScanObservation{}}
		if !row.EndedAt.Valid {
			a.OpenChange = row.ActiveChangeID
		}
		for _, o := range observations {
			if o.ChangeID == row.ID {
				a.Observations = append(a.Observations, o)
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// promoteChangeIn joins an already-authorized human act or landing. Promotion
// is irreversible: no operation turns ordinary work back into disposable work.
func promoteChangeIn(ctx context.Context, tx *database.Tx, project, change ids.ID) error {
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, project); err != nil {
		return err
	}
	row, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: change, ProjectID: project})
	if err != nil {
		return database.Missing(err)
	}
	if row.PurgePending {
		return missingChange{change}
	}
	if !row.Automated {
		return nil
	}
	n, err := q.PromoteAutomatedChange(ctx, workflowsql.PromoteAutomatedChangeParams{ID: change, ProjectID: project})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: automated Change promotion was suppressed", ErrChangeTransition)
	}
	return nil
}

// TouchChangeIn joins an authorized person's write from another owner. An
// agent acting through their credentials does not make automated work human work.
func TouchChangeIn(ctx context.Context, tx *database.Tx, project, change ids.ID) error {
	if !humanTouch(ctx) {
		return nil
	}
	return promoteChangeIn(ctx, tx, project, change)
}

func promoteChange(ctx context.Context, db *database.DB, project, change ids.ID) error {
	return db.InTx(ctx, func(tx *database.Tx) error { return promoteChangeIn(ctx, tx, project, change) })
}

func humanTouch(ctx context.Context) bool {
	caller, ok := access.CallerFrom(ctx)
	return ok && caller.Verified && caller.Principal.Kind == "user" && caller.Principal.Actor().Assents()
}

// These helpers belong only on commands, never permission/offer reads.
func (s *Service) touchChange(ctx context.Context, project, change ids.ID) error {
	if !humanTouch(ctx) || change.IsZero() {
		return nil
	}
	return promoteChange(ctx, s.db, project, change)
}
func (s *Service) touchRun(ctx context.Context, run ids.ID) error {
	if !humanTouch(ctx) {
		return nil
	}
	project, err := runProject(ctx, s.db, run)
	if err != nil {
		return err
	}
	change, err := workflowsql.New(s.db.SQL()).RunChangeMeta(ctx, workflowsql.RunChangeMetaParams{ID: run, ProjectID: project})
	if err != nil {
		return err
	}
	return s.touchChange(ctx, project, change)
}
