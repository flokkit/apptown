package wow

import (
	"context"
	"encoding/json"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/strs"
)

type CatalogPatch struct {
	Description *string     `json:"description"`
	Definition  *CatalogDef `json:"definition"`
}

// EditCatalog publishes metadata and an optional new definition as one edit.
// Authority is supplied by the application caller, just as for catalog creation.
func (s *Service) EditCatalog(ctx context.Context, id ids.ID, patch CatalogPatch, actor string) (*CatalogChange, error) {
	now := s.catalog.now().UTC()
	if patch.Definition != nil {
		copy := *patch.Definition
		if err := copy.Validate(); err != nil {
			return nil, err
		}
		patch.Definition = &copy
	}
	var out CatalogChange
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		held, err := q.CatalogByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if patch.Description != nil {
			if err := oneEdit(q.DescribeCatalogChange(ctx, wowsql.DescribeCatalogChangeParams{ID: id, Description: *patch.Description, At: now})); err != nil {
				return err
			}
		}
		if patch.Definition != nil {
			current, err := q.CatalogVersion(ctx, wowsql.CatalogVersionParams{CatalogID: id, Version: held.LatestVersion})
			if err != nil {
				return err
			}
			var before CatalogDef
			if err := json.Unmarshal(current.DefinitionJson, &before); err != nil {
				return err
			}
			prior, err := strs.JSON(before)
			if err != nil {
				return err
			}
			next, err := strs.JSON(*patch.Definition)
			if err != nil {
				return err
			}
			if prior != next {
				if err := bumpCatalogIn(ctx, tx, held, *patch.Definition, actor, now); err != nil {
					return err
				}
			}
		}
		row, err := q.CatalogByName(ctx, held.Name)
		if err != nil {
			return err
		}
		out = catalogRecord(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// bumpCatalogIn records the change's next version, stamped at the owner's
// clock.
func bumpCatalogIn(ctx context.Context, tx *database.Tx, r wowsql.CatalogChange, def CatalogDef, who string, at time.Time) error {
	q := wowsql.New(tx.SQL())
	next := r.LatestVersion + 1
	definition, err := strs.JSON(def)
	if err != nil {
		return err
	}
	if _, err := q.InsertCatalogVersion(ctx, wowsql.InsertCatalogVersionParams{ID: ids.New(at.UTC()), CatalogID: r.ID, Version: next, Definition: []byte(definition), At: at.UTC()}); err != nil {
		return err
	}
	if err := oneEdit(q.AdvanceCatalogVersion(ctx, wowsql.AdvanceCatalogVersionParams{ID: r.ID, Version: next, At: at.UTC()})); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "catalog.updated", r.Name, map[string]any{"version": next, "status": CatalogDraft.String()})
}
