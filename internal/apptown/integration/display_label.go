package integration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// EditDisplayLabel changes presentation only. Its caller authorizes connector
// management as for Save; configuration versions and credential binding stay put.
func (s *Bindings) EditDisplayLabel(ctx context.Context, id ids.ID, expected, value, who string) (*Binding, error) {
	label, err := strs.DisplayLabel(value)
	if err != nil {
		return nil, fault.New(fault.Invalid, err.Error())
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Binding, error) {
		row, err := integrationsql.New(tx.SQL()).EditBindingDisplayLabel(ctx, integrationsql.EditBindingDisplayLabelParams{ID: id, ExpectedLabel: expected, DisplayLabel: label})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.Conflict, "connection label changed or connection no longer exists; reload before editing")
		}
		if err != nil {
			return nil, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "integration.label-edited", row.Name, map[string]any{"id": id, "before": expected, "label": label}); err != nil {
			return nil, err
		}
		binding := bindingRecord(row)
		return &binding, nil
	})
}
