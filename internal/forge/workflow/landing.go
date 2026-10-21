package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

var ErrLandingAcceptanceChanged = errors.New("accepted Work or its pinned landing handoff changed")

type landings struct {
	db        *database.DB
	adoptions LandingAdoptions
	// eligible decides whether the accepter's yes counts for a landing
	// approval. Without it no acceptance is carried.
	eligible approvalEligibility
	// adds reads what the landing's candidate adds; an acceptance counts
	// only if it recorded the same list.
	adds approvalAdds
	clocked
}
type pendingLanding struct {
	Work       *Run
	LandingID  ids.ID
	Superseded bool
}
type landingAcceptanceReceipt struct {
	AcceptedBy    string `json:"acceptedBy"`
	WorkRun       ids.ID `json:"workRun"`
	Stage         string `json:"stage"`
	RetryRevision int64  `json:"retryRevision"`
}

func (s landings) pending(ctx context.Context, workID ids.ID) (pendingLanding, error) {
	var p pendingLanding
	work, err := runRecord(workflowsql.New(s.db.SQL()).RunByID(ctx, workflowsql.RunByIDParams{ID: workID}))
	if err != nil {
		return p, err
	}
	p.Work = work
	row, err := workflowsql.New(s.db.SQL()).LandingPending(ctx, workID)
	p.LandingID, p.Superseded = row.LandingID, row.Superseded
	return p, err
}
func acceptedWork(run *Run) error {
	spec, err := run.Definition()
	if err != nil {
		return err
	}
	if run.Status != RunDone || !run.ParentRunID.IsZero() || run.ChangeID.IsZero() || spec.Role != "working" {
		return ErrLandingAcceptanceChanged
	}
	return nil
}

// apply records the handoff from accepted Work to its landing: the applied
// marker, both handoff receipts and any adoption receipt, committed together.
// It casts no votes. The acceptance is judged at each landing stage by carry
// (acceptance_carry.go), the one place that decides whether it counts. A
// retry returns only the stage/revision originally named by the receipt.
func (s landings) apply(ctx context.Context, workID, landingID ids.ID) (*Run, error) {
	observed, err := runRecord(workflowsql.New(s.db.SQL()).RunByID(ctx, workflowsql.RunByIDParams{ID: workID}))
	if err != nil {
		return nil, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Run, error) {
		work, err := lockRunSnapshotIn(ctx, tx, observed)
		if err != nil {
			return nil, err
		}
		q := workflowsql.New(tx.SQL())
		superseded, err := q.LandingSuperseded(ctx, work.ID)
		if err != nil {
			return nil, err
		}
		if superseded {
			return nil, nil
		}
		if err := acceptedWork(work); err != nil {
			return nil, err
		}
		handoff, err := q.LandingForUpdate(ctx, workID)
		if err != nil {
			return nil, err
		}
		selected, applied := handoff.LandingRunID, handoff.AcceptanceApplied
		if selected != landingID || landingID.Compare(workID) <= 0 {
			return nil, ErrLandingAcceptanceChanged
		}
		landing, err := runRecord(workflowsql.New(tx.SQL()).RunForUpdate(ctx, landingID))
		if err != nil {
			return nil, err
		}
		spec, err := landing.Definition()
		if err != nil {
			return nil, err
		}
		if landing.ProjectID != work.ProjectID || landing.ChangeID != work.ChangeID || !landing.ParentRunID.IsZero() || spec.Role != "landing" || len(spec.Stages) == 0 {
			return nil, ErrLandingAcceptanceChanged
		}
		change, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: work.ChangeID})
		if err != nil {
			return nil, err
		}
		changeProject := change.ProjectID
		if changeProject != work.ProjectID {
			return nil, ErrLandingAcceptanceChanged
		}
		if applied {
			return landingAcceptanceAdvanceIn(ctx, tx, work.ID, landing)
		}
		accepted, err := acceptanceIn(ctx, tx, work.ID)
		if err != nil {
			return nil, err
		}
		count, err := q.LandingMarkApplied(ctx, workflowsql.LandingMarkAppliedParams{WorkRunID: work.ID, LandingRunID: landing.ID})
		if err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, ErrLandingAcceptanceChanged
		}
		receipt := landingAcceptanceReceipt{accepted.Accepter, work.ID, spec.Stages[0].Name, landing.RetryRevision}
		if err := recordReceiptIn(ctx, tx, landing.ID, "landing.autostarted", receipt, s.now()); err != nil {
			return nil, err
		}
		if err := recordReceiptIn(ctx, tx, work.ID, "landing.autostarted", struct {
			Landing ids.ID `json:"landing"`
		}{landing.ID}, s.now()); err != nil {
			return nil, err
		}
		if err := s.recordLandingAdoptionIn(ctx, tx, work, landing); err != nil {
			return nil, err
		}
		if landing.Status != RunRunning || landing.CurrentStage != receipt.Stage {
			return nil, nil
		}
		return landing, nil
	})
}

// acceptance is who accepted the work and what they were shown the landing
// would ask of them.
type acceptance struct {
	Accepter string
	// Who is the person who accepted and Via how, from their vote; a vote
	// recorded before principals has neither.
	Who   access.Actor
	Shown []string
	// Source is the commit the acceptance was given on; empty for one
	// recorded before acceptances named their commit.
	Source string
	// Adds is what the accepter was shown the change adds; nil for an
	// acceptance recorded before the list existed.
	Adds *ChangeAdds
	// InAdvance is set when the yes was given in advance (in_advance.go):
	// it counts only for approvals that say what such a yes means, and only
	// where the landing passed outright.
	InAdvance bool
}

func acceptanceIn(ctx context.Context, tx *database.Tx, workID ids.ID) (acceptance, error) {
	raw, err := workflowsql.New(tx.SQL()).LandingAcceptedActorEvidence(ctx, workflowsql.LandingAcceptedActorEvidenceParams{RunID: workID, Status: StatusPass.String()})
	if errors.Is(err, sql.ErrNoRows) {
		return acceptance{Accepter: "apptown-system", Who: access.Actor{Name: "apptown-system"}}, nil
	}
	if err != nil {
		return acceptance{}, err
	}
	evidence, err := ReadApprovalEvidence(raw)
	if err != nil {
		return acceptance{}, err
	}
	if evidence.Approver == "" {
		return acceptance{Accepter: "apptown-system", Who: access.Actor{Name: "apptown-system"}}, nil
	}
	out := acceptance{Accepter: evidence.Approver}
	for _, vote := range evidence.Decisions {
		if vote.Approver == evidence.Approver && vote.Decision == VoteApprove {
			out.Shown, out.Source, out.Adds, out.InAdvance = vote.AlsoApproves, vote.Candidate, vote.Adds, vote.InAdvance != ""
			out.Who = access.Actor{Principal: vote.Principal, Name: vote.Approver, Via: vote.Via}
		}
	}
	return out, nil
}
func landingAcceptanceAdvanceIn(ctx context.Context, tx *database.Tx, workID ids.ID, landing *Run) (*Run, error) {
	if landing.Status != RunRunning {
		return nil, nil
	}
	raw, err := workflowsql.New(tx.SQL()).LandingAcceptanceReceipt(ctx, workflowsql.LandingAcceptanceReceiptParams{RunID: landing.ID, WorkRunID: fmt.Sprint(workID)})
	if err != nil {
		return nil, err
	}
	var receipt landingAcceptanceReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if receipt.WorkRun != workID || receipt.Stage == "" || receipt.AcceptedBy == "" || receipt.RetryRevision < 0 {
		return nil, ErrLandingAcceptanceChanged
	}
	if landing.CurrentStage != receipt.Stage || landing.RetryRevision != receipt.RetryRevision {
		return nil, nil
	}
	return landing, nil
}
func (s landings) recordLandingAdoptionIn(ctx context.Context, tx *database.Tx, work, landing *Run) error {
	if work.AdoptionID.IsZero() || landing.AdoptionID == work.AdoptionID {
		return nil
	}
	if s.adoptions == nil {
		return fmt.Errorf("landing adoption observations are unavailable")
	}
	pair, err := s.adoptions.RevisionsIn(ctx, tx, AdoptionPairRequest{ProjectID: work.ProjectID, FromID: work.AdoptionID, ToID: landing.AdoptionID})
	if err != nil {
		return err
	}
	from, to := pair.From, pair.To
	return recordReceiptIn(ctx, tx, landing.ID, "adoption.moved", struct {
		From int64  `json:"from"`
		To   int64  `json:"to"`
		Note string `json:"note"`
	}{from, to, fmt.Sprintf("the project's way of working moved from revision %d to %d while this change was worked; the landing judges against revision %d", from, to, to)}, s.now())
}

// LandingAdoptions supplies the revision labels of the two immutable pins.
// It is read in the same transaction as the acceptance and its receipts.
type AdoptionPairRequest struct{ ProjectID, FromID, ToID ids.ID }
type AdoptionPair struct{ From, To int64 } // revisions
type LandingAdoptions interface {
	RevisionsIn(context.Context, database.Queryer, AdoptionPairRequest) (AdoptionPair, error)
}

func newLandings(db *database.DB, adoptions LandingAdoptions) landings {
	return landings{db: db, adoptions: adoptions}
}

func completionNeedsVote(step *Step, who access.Actor) (bool, error) {
	if step.Type != "approval" || step.Status == StatusPass || step.Status == StatusFail {
		return false, nil
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return false, err
	}
	return !evidence.hasVoted(ApprovalVote{Approver: who.Name, Principal: who.Principal}), nil
}
