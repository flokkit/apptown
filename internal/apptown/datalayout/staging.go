package datalayout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/durable"
)

const metadataName = ".repository-layout-metadata"

// stageMetadata runs only before any repository moves. A partial, uncommitted
// stage can be rebuilt from unchanged source metadata; committed exchanges are
// never regenerated after they have become migration evidence.
func (j *journal) stageMetadata(ctx context.Context) error {
	container := filepath.Join(j.Root, metadataName)
	if err := os.Mkdir(container, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(container)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("metadata container is not a directory")
	}
	parent := filepath.Join(container, j.Nonce)
	if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err = os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("layout metadata staging root is not a directory")
	}
	if err := durable.SyncDir(j.Root); err != nil {
		return err
	}
	count := len(j.Inventory.Linked) + len(j.Inventory.Borrowed)
	if len(j.Exchanges) > count {
		return errors.New("layout has more metadata exchanges than owned workspaces")
	}
	for index := len(j.Exchanges); index < count; index++ {
		work := filepath.Join(parent, strconv.Itoa(index))
		// The journal's staging phase owns these deterministic scratch directories.
		// They have not yet supplied installed metadata or received original backups.
		if err := os.RemoveAll(work); err != nil {
			return err
		}
		if err := os.Mkdir(work, 0o700); err != nil {
			return err
		}
		if err := durable.SyncDir(parent); err != nil {
			return err
		}
		stage, backup := filepath.Join(work, "stage"), filepath.Join(work, "backup")
		if err := os.Mkdir(backup, 0o700); err != nil {
			return err
		}

		var exchange repositorylayout.MetadataExchange
		if index < len(j.Inventory.Linked) {
			linked := j.Inventory.Linked[index]
			if err := linked.StageStandalone(ctx, stage); err != nil {
				return err
			}
			finalHub := ""
			for _, mapping := range j.Inventory.Mappings {
				if mapping.Source(j.Root) == linked.Hub {
					finalHub = mapping.Reference(j.Root, j.Root).After
					break
				}
			}
			if finalHub == "" {
				return fmt.Errorf("linked workspace %s has no identified repository mapping", linked.Folder)
			}
			exchange, err = linked.PlanExchange(stage, finalHub, backup)
		} else {
			borrowed := j.Inventory.Borrowed[index-len(j.Inventory.Linked)]
			if err := borrowed.StageStandalone(ctx, stage); err != nil {
				return err
			}
			exchange, err = borrowed.PlanExchange(stage, backup)
		}
		if err != nil {
			return err
		}

		if err := errors.Join(durable.SyncDir(work), durable.SyncDir(parent), durable.SyncDir(container)); err != nil {
			return err
		}
		j.Exchanges = append(j.Exchanges, exchange)
		if err := j.save(); err != nil {
			return err
		}
	}
	if err := durable.SyncDir(container); err != nil {
		return err
	}
	return j.advance("moving")
}
