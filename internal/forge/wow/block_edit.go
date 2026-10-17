package wow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/strs"
)

type BlockPatch struct {
	BodyMD *string              `json:"bodyMd"`
	Tags   *string              `json:"tags"`
	Checks *[]check.Declaration `json:"checks"`
}

var ErrStockBlock = fault.New(fault.Conflict, "a forge-shipped (stock) guidance block is read-only and auto-upgrades on release")

// EditBlock keeps tags and the entire prose/check version in one transaction.
// Comparisons happen after the row lock, so parallel partial edits preserve
// each other's omitted fields and identical edits do not publish twice.
func (s *Service) EditBlock(ctx context.Context, id ids.ID, patch BlockPatch, actor string) (*Block, error) {
	if patch.Checks != nil {
		if err := check.Validate(*patch.Checks); err != nil {
			return nil, err
		}
	}
	var out Block
	now := s.library.now().UTC()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		held, err := q.BlockByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if held.Origin == "stock" {
			return fmt.Errorf("%q: %w", held.Name, ErrStockBlock)
		}
		if patch.Tags != nil {
			if err := oneEdit(q.SetBlockTags(ctx, wowsql.SetBlockTagsParams{ID: id, Tags: NormalizeTags(*patch.Tags), At: now})); err != nil {
				return err
			}
		}
		if patch.BodyMD != nil || patch.Checks != nil {
			current, err := q.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: id, Version: held.LatestVersion})
			if err != nil {
				return err
			}
			var before []check.Declaration
			if err := json.Unmarshal(current.ChecksJson, &before); err != nil {
				return err
			}
			body, checks := current.BodyMd, before
			if patch.BodyMD != nil {
				body = *patch.BodyMD
			}
			if patch.Checks != nil {
				checks = *patch.Checks
			}
			encode := func(v []check.Declaration) ([]byte, error) {
				if len(v) == 0 {
					return []byte("[]"), nil
				}
				text, err := strs.JSON(v)
				return []byte(text), err
			}
			nextChecks, err := encode(checks)
			if err != nil {
				return err
			}
			priorChecks, err := encode(before)
			if err != nil {
				return err
			}
			if body != current.BodyMd || !bytes.Equal(priorChecks, nextChecks) {
				if err := bumpBlockIn(ctx, tx, held, body, string(nextChecks), actor, now); err != nil {
					return err
				}
			}
		}
		row, err := q.BlockByID(ctx, id)
		if err != nil {
			return err
		}
		out = blockRecord(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// bumpBlockIn records the block's next version, stamped at (the owner's
// clock: adoptions resolve blocks as of their own time by it).
func bumpBlockIn(ctx context.Context, tx *database.Tx, r wowsql.GuidanceBlock, body, checks, who string, at time.Time) error {
	q := wowsql.New(tx.SQL())
	next := r.LatestVersion + 1
	if _, err := q.InsertBlockVersion(ctx, wowsql.InsertBlockVersionParams{ID: ids.New(at.UTC()), BlockID: r.ID, Version: next, Body: body, Checks: []byte(checks), At: at.UTC()}); err != nil {
		return err
	}
	if err := oneEdit(q.AdvanceBlockVersion(ctx, wowsql.AdvanceBlockVersionParams{ID: r.ID, Version: next, At: at.UTC()})); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "guidance.block.updated", r.Name, map[string]any{"blockId": r.ID, "version": next})
}
