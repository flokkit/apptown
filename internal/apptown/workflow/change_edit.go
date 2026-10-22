package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

var errChangeEditConflict = fault.New(fault.Conflict, "Change edit was not recorded")

type ChangeEdit struct {
	ProjectID, ID            ids.ID
	Title, Body, ExternalRef *string
	// ExpectedRevision is the edit revision the edit started from. It is
	// required: without it an edit could overwrite changes it never saw.
	ExpectedRevision *int64
}

var (
	errChangeEditRevisionRequired = fault.New(fault.Invalid, "send the edit revision the edit started from (expectedEditRevision)")
	// errChangeEditMoved carries a code so a client can tell it from other
	// conflicts and offer the person the current text.
	errChangeEditMoved error = changeEditMoved{}
)

type changeEditMoved struct{}

func (changeEditMoved) Error() string {
	return "this change's description was edited since this edit started; review the current text before saving again"
}
func (changeEditMoved) FaultKind() fault.Kind { return fault.Conflict }
func (changeEditMoved) FaultCode() string     { return "change-edit-moved" }

// Human descriptions remain editable after completion. Selected fields and the
// receipt commit together; omitted fields keep their values. The edit must
// name the edit revision it started from; a Change edited since is refused
// before anything is written, and the edit advances the revision. The
// distiller's generated titles do not advance it. A human title takes
// ownership from the distiller, while an empty title is ignored.
func (s changeStore) edit(ctx context.Context, in ChangeEdit, who string) (*Change, error) {
	if in.ProjectID.IsZero() || in.ID.IsZero() {
		return nil, fmt.Errorf("invalid Change edit scope")
	}
	var ref *string
	if in.ExternalRef != nil {
		value := strings.TrimSpace(*in.ExternalRef)
		ref = &value
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Change, error) {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, in.ProjectID); err != nil {
			return nil, err
		}
		held, err := workflowsql.New(tx.SQL()).ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: in.ID, ProjectID: in.ProjectID})
		if err != nil {
			return nil, err
		}
		if in.ExpectedRevision == nil {
			return nil, errChangeEditRevisionRequired
		}
		if *in.ExpectedRevision != held.EditRevision {
			return nil, errChangeEditMoved
		}
		if in.Title != nil {
			if _, err := editChangeTitleIn(ctx, tx, in.ProjectID, in.ID, changeTitleEdit{act: pinTitle, words: *in.Title}); err != nil {
				return nil, err
			}
		}
		params := workflowsql.EditChangeDescriptionParams{ID: in.ID, ProjectID: in.ProjectID}
		if in.Body != nil {
			params.Body = sql.NullString{String: *in.Body, Valid: true}
		}
		if ref != nil {
			params.ExternalRef = sql.NullString{String: *ref, Valid: true}
		}
		n, err := workflowsql.New(tx.SQL()).EditChangeDescription(ctx, params)
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, errChangeEditConflict
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, who, in.ProjectID, "change.edited", fmt.Sprintf("change %d", in.ID), nil); err != nil {
			return nil, err
		}
		return readChange(ctx, tx, in.ID)
	})
}
