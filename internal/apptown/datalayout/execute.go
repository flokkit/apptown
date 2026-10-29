package datalayout

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/durable"
)

// Relocate repairs the absolute local references a restored data directory
// carries when it was restored into a different path. It must run under the
// daemon's database exclusion, before constructing application recovery or
// serving requests. oldRoot is the durable archive manifest's data directory
// on restore and root otherwise; with the same root there is nothing to do.
func Relocate(ctx context.Context, db *database.DB, root, oldRoot, instance string, observers Observers) error {
	if _, err := ReadVersion(root); err != nil {
		return err
	}
	j, found, err := readJournal(root, instance)
	if err != nil {
		return err
	}
	if !found {
		if oldRoot == root {
			return nil
		}
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(oldRoot) || filepath.Clean(oldRoot) != oldRoot || instance == "" {
			return errors.New("relocation requires installation identity and absolute clean roots")
		}
		inventory, err := inspect(ctx, db, root, oldRoot, observers)
		if err != nil {
			return err
		}
		j = journal{Nonce: rand.Text(), Format: 2, Instance: instance, Root: root, OriginalRoot: oldRoot, Phase: "references", Inventory: inventory}
		if err := j.save(); err != nil {
			return err
		}
	} else if oldRoot != j.OriginalRoot && j.Phase != "complete" {
		return errors.New("pending relocation belongs to a different restore root")
	}
	if err := j.validatePlan(); err != nil {
		return err
	}
	if j.Phase != "complete" {
		if err := j.verifyOwners(ctx, db); err != nil {
			return err
		}
		if err := refuseWriters(ctx, db, root, observers); err != nil {
			return err
		}
	}
	for {
		switch j.Phase {
		case "references":
			for _, edit := range j.Inventory.Git {
				if err := edit.Apply(ctx); err != nil {
					return err
				}
			}
			for _, edit := range j.Inventory.Consoles {
				if err := edit.Apply(); err != nil {
					return err
				}
			}
			if err := db.InTx(ctx, func(tx *database.Tx) error {
				for _, edit := range j.Inventory.Publications {
					if err := edit.Before.RelocateIn(ctx, tx, edit.After); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			if err := j.advance("verifying"); err != nil {
				return err
			}
		case "verifying":
			if err := j.verify(); err != nil {
				return err
			}
			if err := j.advance("complete"); err != nil {
				return err
			}
		case "complete":
			// Keep the original-root continuation until AfterRestore and its durable
			// intent have settled. Normal startup finalizes immediately before New.
			return nil
		default:
			return errors.New("unrecognized relocation phase")
		}
	}
}

func (j journal) verify() error {
	for _, mapping := range j.Inventory.Mappings {
		target := mapping.Reference(j.Root, j.Root).After
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) && mapping.Missing {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("relocated repository is not a directory: %s", target)
		}
	}
	return nil
}

// Finalize is called under the same daemon exclusion after any pending restore
// has completed. A restart after restore intent removal can finish this cleanup
// from the complete journal without replaying filesystem or database edits.
func Finalize(root, instance string) error {
	j, found, err := readJournal(root, instance)
	if err != nil || !found {
		return err
	}
	if j.Phase != "complete" {
		return errors.New("cannot finalize an incomplete relocation")
	}
	if err := j.validatePlan(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, journalName)); err != nil {
		return err
	}
	return durable.SyncDir(root)
}
