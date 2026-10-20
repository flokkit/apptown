package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/stamp"

	"flokkit.io/forge/internal/database"
)

var (
	ErrAlreadyConfirmed = fault.New(fault.Conflict, "verdict is already confirmed")
	ErrNotConfirmable   = fault.New(fault.Unprocessable, "only an AI does-not-conform verdict can be confirmed")
	ErrVerdictChanged   = fault.New(fault.Conflict, "previous verdict or confirmation changed; run the review again")
)

type confirmationRequest struct {
	VerdictID ids.ID
	By, Note  string
}

// One typed batch reader serves both required decisions and projections. An
// unavailable confirmation record cannot look like an unconfirmed AI flag.
func readAdjudications(ctx context.Context, db database.Queryer, idList []ids.ID) (map[ids.ID]Adjudication, error) {
	out := map[ids.ID]Adjudication{}
	if len(idList) == 0 {
		return out, nil
	}
	rows, err := queries(db).AdjudicationsForVerdicts(ctx, idList)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		a := adjudication(r)
		out[a.VerdictID] = a
	}
	return out, nil
}

func lockConformanceProject(ctx context.Context, tx *database.Tx, id ids.ID) error {
	_, err := queries(tx).LockProjectForReceipt(ctx, id)
	return err
}

// Confirmation and evaluation admission share project-before-verdict locking.
// The verdict is immutable, but its confirmation eligibility must be read after
// locking so concurrent confirmations have exactly one successful caller.
func (s reviewStore) confirm(ctx context.Context, req confirmationRequest) (*Adjudication, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Adjudication, error) {
		named, err := queries(tx).VerdictByID(ctx, req.VerdictID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w %d", ErrNoVerdict, req.VerdictID)
		}
		if err != nil {
			return nil, err
		}
		projectID := named.ProjectID
		if err := lockConformanceProject(ctx, tx, projectID); err != nil {
			return nil, err
		}
		held, err := queries(tx).VerdictInProjectForUpdate(ctx, conformancesql.VerdictInProjectForUpdateParams{ID: req.VerdictID, ProjectID: projectID})
		if err != nil {
			return nil, err
		}
		v, err := verdict(held)
		if err != nil {
			return nil, err
		}
		if !v.Confirmable() {
			return nil, fmt.Errorf("%w (this one is %s by %s)", ErrNotConfirmable, v.State, v.Evaluator)
		}
		existing, err := readAdjudications(ctx, tx, []ids.ID{v.ID})
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("%w: %d", ErrAlreadyConfirmed, v.ID)
		}
		at := s.now()
		confirmer, err := identity.ActorIn(ctx, tx, req.By)
		if err != nil {
			return nil, err
		}
		row, err := queries(tx).InsertAdjudication(ctx, conformancesql.InsertAdjudicationParams{ID: ids.New(stamp.Of(at).Time),
			VerdictID: v.ID, ProjectID: projectID, Kind: "confirmed", By: req.By, ByPrincipal: confirmer.Principal, ByVia: confirmer.Via,
			Note: strings.TrimSpace(req.Note), CreatedAt: stamp.Of(at).Time,
		})
		if err != nil {
			return nil, err
		}
		stored := adjudication(row)
		a := &stored
		if err := audit.At(at).RecordIn(ctx, tx, a.By, projectID, "conformance.confirmed", fmt.Sprint(v.ID), confirmationReceipt{Block: v.Block, Version: v.Version, Check: v.Check, SHA: v.SHA, Note: a.Note}); err != nil {
			return nil, err
		}
		return a, nil
	})
}

// Attribution facts are prepared before admission, without holding database
// locks during Git work. The store rechecks which verdict is being superseded.
type FixAttribution struct {
	Changes         []ids.ID
	Commits         string
	RemediationItem ids.ID
	// AcceptedBy is who accepted the landed change, as their vote recorded
	// them.
	AcceptedBy access.Actor
}

type verdictAdmission struct {
	Verdict    Verdict
	PreviousID ids.ID
	Fix        *FixAttribution
}

func fixRequired(ctx context.Context, db database.Queryer, prev, next Verdict) (bool, error) {
	if !prev.FixedBy(next) {
		return false, nil
	}
	if prev.Evaluator != "llm" {
		return true, nil
	}
	adj, err := readAdjudications(ctx, db, []ids.ID{prev.ID})
	return len(adj) > 0, err
}

// recordFixIn records a fix at AppTown's clock (at).
func recordFixIn(ctx context.Context, tx *database.Tx, req verdictAdmission, saved Verdict, at time.Time) error {
	f := req.Fix
	if f == nil {
		return nil
	}
	changes, err := json.Marshal(f.Changes)
	if err != nil {
		return err
	}
	accepter := f.AcceptedBy
	if _, err := queries(tx).InsertFix(ctx, conformancesql.InsertFixParams{ID: ids.New(stamp.Of(at).Time),
		ProjectID: saved.ProjectID, CheckKey: CheckKey(saved.Block, saved.Version, saved.Check),
		BeforeVerdictID: req.PreviousID, AfterVerdictID: saved.ID, ChangeIdsJson: changes,
		Commits: f.Commits, RemediationItemID: f.RemediationItem, AcceptedBy: accepter.Name,
		AcceptedByPrincipal: accepter.Principal, AcceptedByVia: accepter.Via, CreatedAt: stamp.Of(at).Time,
	}); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, "apptown-system", saved.ProjectID, "conformance.fixed", saved.Check, fixReceipt{Changes: f.Changes, Review: saved.ReviewID, Before: req.PreviousID, After: saved.ID})
}

// Required audit payloads are typed at the domain boundary.
type confirmationReceipt struct {
	Block   string `json:"block"`
	Version int    `json:"version"`
	Check   string `json:"check"`
	SHA     string `json:"sha"`
	Note    string `json:"note"`
}
type fixReceipt struct {
	Changes []ids.ID `json:"changes"`
	Review  ids.ID   `json:"review"`
	Before  ids.ID   `json:"before"`
	After   ids.ID   `json:"after"`
}
