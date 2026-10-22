package adrs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type store struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a record and its receipt; nil is the real one
}

func (s store) now() time.Time { return clock.Or(s.clock)().UTC() }

func lockProject(ctx context.Context, tx *database.Tx, id ids.ID) error {
	_, err := queries(tx).ProjectForUpdate(ctx, id)
	return err
}
func insertRecord(ctx context.Context, tx *database.Tx, in Create, supersedesID ids.ID, at time.Time) (Record, error) {
	if strings.TrimSpace(in.Title) == "" {
		return Record{}, fmt.Errorf("%w: title is required", ErrInput)
	}
	if err := requireChange(ctx, tx, in.ProjectID, in.ChangeID); err != nil {
		return Record{}, err
	}
	number, err := queries(tx).NextADRNumber(ctx, in.ProjectID)
	if err != nil {
		return Record{}, err
	}
	saved, err := queries(tx).InsertADR(ctx, adrssql.InsertADRParams{ID: ids.New(at), ProjectID: in.ProjectID, Number: number, Title: in.Title, Status: Draft.String(), BodyMd: in.BodyMD, Tags: normalizeTags(in.Tags), SupersedesID: supersedesID, ChangeID: in.ChangeID, At: at})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrConflict
	}
	if err != nil {
		return Record{}, err
	}
	return readRecord(ctx, tx, saved.ID, in.ProjectID, false)
}
func (s store) create(ctx context.Context, in Create, who string) (Record, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Record, error) {
		if err := lockProject(ctx, tx, in.ProjectID); err != nil {
			return Record{}, err
		}
		v, err := insertRecord(ctx, tx, in, ids.Nil, now)
		if err != nil {
			return Record{}, err
		}
		err = audit.At(now).RecordIn(ctx, tx, who, in.ProjectID, "adr.created", fmt.Sprintf("ADR-%03d %s", v.Number, v.Title), struct {
			ID ids.ID `json:"adrId"`
		}{v.ID})
		return v, err
	})
}
func (s store) successor(ctx context.Context, projectID ids.ID, in Successor, who string) (Record, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Record, error) {
		if err := lockProject(ctx, tx, projectID); err != nil {
			return Record{}, err
		}
		old, err := readRecord(ctx, tx, in.ID, projectID, true)
		if err != nil {
			return Record{}, err
		}
		if old.Status != Accepted {
			return Record{}, fmt.Errorf("%w: only accepted ADRs may have a successor", ErrTransition)
		}
		if in.Title == "" {
			in.Title = old.Title
		}
		if in.BodyMD == "" {
			in.BodyMD = fmt.Sprintf("Supersedes ADR-%03d.\n\n%s", old.Number, old.BodyMD)
		}
		v, err := insertRecord(ctx, tx, Create{ProjectID: projectID, Title: in.Title, BodyMD: in.BodyMD, Tags: old.Tags}, old.ID, now)
		if err != nil {
			return Record{}, err
		}
		err = audit.At(now).RecordIn(ctx, tx, who, projectID, "adr.superseding", fmt.Sprintf("ADR-%03d supersedes ADR-%03d", v.Number, old.Number), struct {
			New ids.ID `json:"newAdrId"`
			Old ids.ID `json:"oldAdrId"`
		}{v.ID, old.ID})
		return v, err
	})
}

type stateChange struct {
	From State `json:"from"`
	To   State `json:"to"`
}
type editReceipt struct {
	ID         ids.ID       `json:"adrId"`
	State      *stateChange `json:"status,omitempty"`
	BodyEdited bool         `json:"bodyEdited,omitempty"`
	ChangeID   *ids.ID      `json:"changeId,omitzero"`
}

func (s store) edit(ctx context.Context, projectID ids.ID, in Edit, who string) (Record, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Record, error) {
		if err := lockProject(ctx, tx, projectID); err != nil {
			return Record{}, err
		}
		old, err := readRecord(ctx, tx, in.ID, projectID, true)
		if err != nil {
			return Record{}, err
		}
		v, err := editIn(ctx, tx, old, in, now)
		if err != nil {
			return Record{}, err
		}
		receipt := editReceipt{ID: v.ID, BodyEdited: in.BodyMD != nil, ChangeID: in.ChangeID}
		if v.Status != old.Status {
			receipt.State = &stateChange{old.Status, v.Status}
		}
		err = audit.At(now).RecordIn(ctx, tx, who, projectID, "adr.updated", fmt.Sprintf("ADR-%03d %s", v.Number, v.Title), receipt)
		return v, err
	})
}

// textUnmoved checks, on the freshly locked row, that each text field the
// edit writes still holds the text the edit started from.
func textUnmoved(old Record, in Edit) error {
	check := func(write, expected *string, stored string, norm func(string) string) error {
		if write == nil {
			return nil
		}
		if expected == nil {
			return ErrTextExpected
		}
		if norm(*expected) != stored {
			return ErrTextMoved
		}
		return nil
	}
	same := func(s string) string { return s }
	if err := check(in.Title, in.Expected.Title, old.Title, same); err != nil {
		return err
	}
	if err := check(in.BodyMD, in.Expected.BodyMD, old.BodyMD, same); err != nil {
		return err
	}
	return check(in.Tags, in.Expected.Tags, old.Tags, normalizeTags)
}

// Every state change, including predecessor retirement, passes this operation.
// It consumes a freshly locked row and owns the single guarded full-record write.
// Public commands cannot ask for the private retire act directly.
func editIn(ctx context.Context, tx *database.Tx, old Record, in Edit, at time.Time) (Record, error) {
	v := old
	if old.Status.Locked() && (in.Title != nil || in.BodyMD != nil) {
		return Record{}, ErrImmutable
	}
	if err := textUnmoved(old, in); err != nil {
		return Record{}, err
	}
	if in.Title != nil {
		if strings.TrimSpace(*in.Title) == "" {
			return Record{}, fmt.Errorf("%w: title is required", ErrInput)
		}
		v.Title = *in.Title
	}
	if in.BodyMD != nil {
		v.BodyMD = *in.BodyMD
	}
	if in.Tags != nil {
		v.Tags = normalizeTags(*in.Tags)
	}
	if in.ChangeID != nil {
		if err := requireChange(ctx, tx, old.ProjectID, *in.ChangeID); err != nil {
			return Record{}, err
		}
		v.ChangeID = *in.ChangeID
	}
	if in.Decision != 0 {
		next, err := old.Status.decide(in.Decision)
		if err != nil {
			return Record{}, err
		}
		v.Status = next
	}
	if v.Status == Accepted && old.Status != Accepted && !old.SupersedesID.IsZero() {
		previous, err := readRecord(ctx, tx, old.SupersedesID, old.ProjectID, true)
		if err != nil {
			return Record{}, err
		}
		if old.Status == Deprecated {
			// Restoring a previously accepted successor preserves its original
			// retirement. It must not retire the predecessor for a second time.
			if previous.Status != Superseded {
				return Record{}, fmt.Errorf("%w: predecessor retirement is missing", ErrConflict)
			}
		} else {
			if previous.Status != Accepted {
				return Record{}, fmt.Errorf("%w: predecessor is no longer accepted", ErrConflict)
			}
			if _, err := editIn(ctx, tx, previous, Edit{ID: previous.ID, Decision: retire}, at); err != nil {
				return Record{}, err
			}
		}
	}
	err := requireOne(queries(tx).EditADR(ctx, adrssql.EditADRParams{Title: v.Title, BodyMd: v.BodyMD, Tags: v.Tags, Status: v.Status.String(), ChangeID: v.ChangeID, ID: old.ID, ProjectID: old.ProjectID, OldStatus: old.Status.String(), At: at}))
	if err != nil {
		return Record{}, err
	}
	return readRecord(ctx, tx, old.ID, old.ProjectID, false)
}
