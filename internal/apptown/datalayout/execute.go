package datalayout

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/gitexec"
)

// Upgrade must run under the daemon's database exclusion, before constructing
// application recovery or serving requests. oldRoot is the durable archive
// manifest's data directory on restore and root otherwise.
func Upgrade(ctx context.Context, db *database.DB, root, oldRoot, instance string, observers Observers) error {
	version, err := ReadVersion(root)
	if err != nil {
		return err
	}
	j, found, err := readJournal(root, instance)
	if err != nil {
		return err
	}
	if !found {
		if version == Version && oldRoot == root {
			return nil
		}
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(oldRoot) || filepath.Clean(oldRoot) != oldRoot || instance == "" {
			return errors.New("layout upgrade requires installation identity and absolute clean roots")
		}
		inventory, err := inspect(ctx, db, root, oldRoot, version == Version, observers)
		if err != nil {
			return err
		}
		if err := repositorylayout.Check(root, instance, inventory.Mappings); err != nil {
			return err
		}
		j = journal{Nonce: rand.Text(), Format: 1, Instance: instance, Root: root, OriginalRoot: oldRoot, FromVersion: version, Phase: "planned", Inventory: inventory}
		if err := j.save(); err != nil {
			return err
		}
	} else if oldRoot != j.OriginalRoot && j.Phase != "complete" {
		return errors.New("pending layout upgrade belongs to a different restore root")
	}
	if err := j.validatePlan(); err != nil {
		return err
	}
	if j.Phase == "complete" && version != Version {
		return errors.New("completed journal has no completed layout marker")
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
		case "planned":
			if err := j.advance("staging"); err != nil {
				return err
			}
		case "staging":
			if err := j.stageMetadata(ctx); err != nil {
				return err
			}
		case "moving":
			moves, err := repositorylayout.Prepare(root, instance, j.Inventory.Mappings)
			if err != nil {
				return err
			}
			if err := moves.Install(); err != nil {
				return err
			}
			// Subsequent authorized edits invalidate the original tree fingerprints.
			// This durable boundary is written before any such edit and skips move
			// fingerprint replay on every later restart.
			if err := j.advance("metadata"); err != nil {
				return err
			}
		case "metadata":
			for _, exchange := range j.Exchanges {
				if err := exchange.Apply(ctx); err != nil {
					return err
				}
			}
			if err := j.advance("references"); err != nil {
				return err
			}
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
			if err := j.verify(ctx); err != nil {
				return err
			}
			if _, err := durable.Write(root, "layout", []byte("2\n"), 0o600, false); err != nil {
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
			return errors.New("unrecognized layout transition")
		}
	}
}

func (j journal) verify(ctx context.Context) error {
	for _, evidence := range j.Inventory.Mains {
		path := ""
		for _, mapping := range j.Inventory.Mappings {
			if mapping.ID == evidence.ProjectID {
				path = mapping.Reference(j.Root, j.Root).After
				break
			}
		}
		if path == "" {
			return fmt.Errorf("main evidence has unknown owner %s", evidence.ProjectID)
		}
		got, err := gitexec.HubRun(ctx, path, "rev-parse", "--verify", "refs/heads/"+evidence.Branch)
		if err != nil || got != evidence.SHA {
			return fmt.Errorf("project %s main changed during relocation: %v", evidence.ProjectID, err)
		}
	}
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
	for _, exchange := range j.Exchanges {
		if err := gitexec.CheckFolder(ctx, exchange.Folder); err != nil {
			return err
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
		return errors.New("cannot finalize incomplete data layout")
	}
	if version, err := ReadVersion(root); err != nil || version != Version {
		return fmt.Errorf("completed layout marker missing: %v", err)
	}
	if err := j.validatePlan(); err != nil {
		return err
	}
	if err := repositorylayout.ClearCompleted(root, instance, j.Inventory.Mappings); err != nil {
		return err
	}
	receiptRoot := filepath.Join(root, metadataName, j.Nonce)
	raw, err := os.ReadFile(filepath.Join(root, journalName))
	if err != nil {
		return err
	}
	if _, err := durable.Write(receiptRoot, "receipt.json", raw, 0o600, false); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, journalName)); err != nil {
		return err
	}
	return durable.SyncDir(root)
}
