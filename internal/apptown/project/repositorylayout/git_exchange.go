package repositorylayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/gitexec"
)

// MetadataExchange is persisted before replacing a linked workspace pointer.
// Backups remain owned by the migration until the complete installation verifies.
type MetadataExchange struct {
	Kind                    string `json:"kind"`
	OriginalDirectoryDigest string `json:"originalDirectoryDigest,omitempty"`
	Folder                  string `json:"folder"`
	Staged                  string `json:"staged"`
	Backup                  string `json:"backup"`
	Admin                   string `json:"admin"`
	PointerDigest           string `json:"pointerDigest"`
	MetadataDigest          string `json:"metadataDigest"`
	AdminDigest             string `json:"adminDigest"`
}

// PlanExchange observes original metadata and a completed standalone stage.
// finalHub names the identified hub after repository relocation.
func (w LinkedWorkspace) PlanExchange(stage, finalHub, backup string) (MetadataExchange, error) {
	out := MetadataExchange{Kind: "linked", Folder: w.Folder, Staged: filepath.Join(stage, ".git"), Backup: backup, Admin: filepath.Join(finalHub, ".git", w.Admin)}
	var err error
	out.PointerDigest, err = pointerDigest(filepath.Join(w.Folder, ".git"))
	if err != nil {
		return out, err
	}
	out.MetadataDigest, err = treeDigest(out.Staged)
	if err != nil {
		return out, err
	}
	out.AdminDigest, err = treeDigest(filepath.Join(w.Hub, ".git", w.Admin))
	if err != nil {
		return out, err
	}
	if err := directory(backup); err != nil {
		return out, err
	}
	entries, err := os.ReadDir(backup)
	if err != nil {
		return out, err
	}
	if len(entries) != 0 {
		return out, errors.New("metadata backup destination is not empty")
	}
	return out, nil
}

// Apply resumes at either side of each rename. It never overwrites an
// unexpected pointer, staged directory, backup or worktree administration entry.
func (e MetadataExchange) Apply(ctx context.Context) error {
	switch e.Kind {
	case "borrowed":
		if e.OriginalDirectoryDigest == "" || e.PointerDigest != "" || e.Admin != "" || e.AdminDigest != "" {
			return errors.New("borrowed metadata exchange has mixed ownership fields")
		}
		return e.applyDirectory(ctx)
	case "linked":
		if e.OriginalDirectoryDigest != "" || e.PointerDigest == "" || e.Admin == "" || e.AdminDigest == "" {
			return errors.New("linked metadata exchange has mixed ownership fields")
		}
	default:
		return errors.New("unrecognized metadata exchange kind")
	}

	for _, path := range []string{e.Folder, e.Backup, filepath.Dir(e.Staged), filepath.Dir(e.Admin)} {
		if err := directory(path); err != nil {
			return err
		}
	}
	pointer := filepath.Join(e.Folder, ".git")
	saved := filepath.Join(e.Backup, "pointer")
	if _, err := os.Lstat(saved); errors.Is(err, fs.ErrNotExist) {
		got, err := pointerDigest(pointer)
		if err != nil {
			return err
		}
		if got != e.PointerDigest {
			return errors.New("workspace pointer changed before metadata exchange")
		}
		if err := os.Rename(pointer, saved); err != nil {
			return err
		}
		if err := errors.Join(durable.SyncDir(e.Folder), durable.SyncDir(e.Backup)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	got, err := pointerDigest(saved)
	if err != nil || got != e.PointerDigest {
		return fmt.Errorf("workspace pointer backup changed: %v", err)
	}
	if err := moveChecked(e.Staged, pointer, e.MetadataDigest); err != nil {
		return err
	}
	if err := gitexec.CheckFolder(ctx, e.Folder); err != nil {
		return err
	}
	// Move only this verified worktree's old administration entry. Do not run
	// worktree prune: unrelated or incomplete workspace evidence must survive.
	return moveChecked(e.Admin, filepath.Join(e.Backup, "admin"), e.AdminDigest)
}

func pointerDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Git pointer is not regular: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (e MetadataExchange) applyDirectory(ctx context.Context) error {
	for _, path := range []string{e.Folder, e.Backup, filepath.Dir(e.Staged)} {
		if err := directory(path); err != nil {
			return err
		}
	}
	original := filepath.Join(e.Folder, ".git")
	saved := filepath.Join(e.Backup, "metadata")
	if _, err := os.Lstat(saved); errors.Is(err, fs.ErrNotExist) {
		if err := verify(original, e.OriginalDirectoryDigest); err != nil {
			return err
		}
		if err := os.Rename(original, saved); err != nil {
			return err
		}
		if err := errors.Join(durable.SyncDir(e.Folder), durable.SyncDir(e.Backup)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := verify(saved, e.OriginalDirectoryDigest); err != nil {
		return err
	}
	if err := moveChecked(e.Staged, original, e.MetadataDigest); err != nil {
		return err
	}
	return gitexec.CheckFolder(ctx, e.Folder)
}
