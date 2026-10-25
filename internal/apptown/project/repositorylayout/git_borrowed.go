package repositorylayout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/gitexec"
)

// BorrowedWorkspace names one independently owned metadata directory whose
// objects borrow only its identified project's hub. It is not an arbitrary
// alternate resolver; nested, HTTP and foreign object sources are refused.
type BorrowedWorkspace struct {
	MetadataDigest string `json:"metadataDigest"`
	ObjectDigest   string `json:"objectDigest"`
	Folder         string `json:"folder"`
	Hub            string `json:"hub"`
	OriginalRoot   string `json:"originalRoot"`
	CurrentRoot    string `json:"currentRoot"`
}

func InspectBorrowedWorkspace(folder, hub, oldRoot, root string) (BorrowedWorkspace, bool, error) {
	var empty BorrowedWorkspace
	for _, value := range []string{folder, hub, oldRoot, root} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return empty, false, errors.New("borrowed workspace needs absolute clean owner paths")
		}
	}
	metadata := filepath.Join(folder, ".git")
	for _, dir := range []string{folder, metadata, filepath.Join(metadata, "objects")} {
		if err := directory(dir); err != nil {
			return empty, false, err
		}
	}
	alternate := filepath.Join(metadata, "objects", "info", "alternates")
	if _, err := os.Lstat(alternate); errors.Is(err, fs.ErrNotExist) {
		return empty, false, nil
	} else if err != nil {
		return empty, false, err
	}
	if err := directory(filepath.Join(metadata, "objects", "info")); err != nil {
		return empty, false, err
	}
	if err := regularMetadataTree(metadata); err != nil {
		return empty, false, err
	}
	for _, name := range []string{"commondir", "gitdir", filepath.Join("objects", "info", "http-alternates")} {
		if _, err := os.Lstat(filepath.Join(metadata, name)); err == nil {
			return empty, false, fmt.Errorf("workspace has unsupported borrowed metadata %s", name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return empty, false, err
		}
	}
	raw, err := os.ReadFile(alternate)
	if err != nil {
		return empty, false, err
	}
	value := strings.TrimSuffix(string(raw), "\n")
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00\"\\") {
		return empty, false, errors.New("workspace object alternate is not one unambiguous local path")
	}
	target := restoredGitPath(filepath.Join(metadata, "objects"), value, oldRoot, root)
	expected := filepath.Join(hub, ".git", "objects")
	for _, dir := range []string{hub, filepath.Join(hub, ".git"), expected, target} {
		if err := directory(dir); err != nil {
			return empty, false, err
		}
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return empty, false, err
	}
	canonicalExpected, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return empty, false, err
	}
	if canonicalTarget != canonicalExpected {
		return empty, false, errors.New("workspace borrows objects from another repository")
	}
	if err := regularMetadataTree(expected); err != nil {
		return empty, false, err
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if _, err := os.Lstat(filepath.Join(expected, "info", name)); err == nil {
			return empty, false, errors.New("hub objects have a nested dependency")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return empty, false, err
		}
	}
	if err := validateObjectStore(expected); err != nil {
		return empty, false, err
	}
	metadataDigest, err := treeDigest(metadata)
	if err != nil {
		return empty, false, err
	}
	objectDigest, err := treeDigest(expected)
	if err != nil {
		return empty, false, err
	}
	return BorrowedWorkspace{Folder: folder, Hub: hub, OriginalRoot: oldRoot, CurrentRoot: root, MetadataDigest: metadataDigest, ObjectDigest: objectDigest}, true, nil
}

// StageStandalone preserves original metadata/config and working files. Only
// the private stage loses its alternate after every owned object is copied.
func (w BorrowedWorkspace) StageStandalone(ctx context.Context, destination string) error {
	observed, found, err := InspectBorrowedWorkspace(w.Folder, w.Hub, w.OriginalRoot, w.CurrentRoot)
	if err != nil {
		return err
	}
	if !found || observed != w {
		return errors.New("borrowed workspace ownership changed before staging")
	}
	if err := gitexec.CheckLinkedConfig(ctx, w.Folder); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	target := filepath.Join(destination, ".git")
	if err := copyMetadata(filepath.Join(w.Folder, ".git"), target); err != nil {
		return err
	}
	objects := filepath.Join(w.Hub, ".git", "objects")
	entries, err := os.ReadDir(objects)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "info" {
			continue
		}
		if err := mergeObjects(filepath.Join(objects, entry.Name()), filepath.Join(target, "objects", entry.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(target, "objects", "info", "alternates")); err != nil {
		return err
	}
	if err := verify(filepath.Join(w.Folder, ".git"), w.MetadataDigest); err != nil {
		return err
	}
	if err := verify(filepath.Join(w.Hub, ".git", "objects"), w.ObjectDigest); err != nil {
		return err
	}
	if err := syncMetadata(target); err != nil {
		return err
	}
	if err := gitexec.CheckFolder(ctx, destination); err != nil {
		return err
	}
	if _, err := gitexec.HubRun(ctx, destination, "fsck", "--full", "--no-dangling"); err != nil {
		return fmt.Errorf("materialized workspace objects are incomplete: %w", err)
	}
	for _, name := range []string{"HEAD", "index", "config"} {
		before, beforeErr := os.ReadFile(filepath.Join(w.Folder, ".git", name))
		after, afterErr := os.ReadFile(filepath.Join(target, name))
		if errors.Is(beforeErr, fs.ErrNotExist) && errors.Is(afterErr, fs.ErrNotExist) {
			continue
		}
		if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
			return fmt.Errorf("materialization changed workspace %s", name)
		}
	}
	return durable.SyncDir(destination)
}
func (w BorrowedWorkspace) PlanExchange(stage, backup string) (MetadataExchange, error) {
	out := MetadataExchange{Kind: "borrowed", Folder: w.Folder, Staged: filepath.Join(stage, ".git"), Backup: backup}
	var err error
	if err := verify(filepath.Join(w.Folder, ".git"), w.MetadataDigest); err != nil {
		return out, err
	}
	out.OriginalDirectoryDigest, err = treeDigest(filepath.Join(w.Folder, ".git"))
	if err != nil {
		return out, err
	}
	out.MetadataDigest, err = treeDigest(out.Staged)
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
func regularMetadataTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("metadata contains a link or special file: %s", path)
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			return fmt.Errorf("metadata is locked: %s", path)
		}
		return nil
	})
}
func validateObjectStore(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if parts[0] == "info" {
			if rel == "info" {
				return nil
			}
			// These are accelerators, not object contents. Do not overlay them onto
			// the workspace's independent object metadata.
			if len(parts) == 2 && (parts[1] == "packs" || parts[1] == "commit-graph") {
				return nil
			}
			if parts[1] == "commit-graphs" {
				return nil
			}
			return fmt.Errorf("unsupported hub object metadata: %s", rel)
		}
		if parts[0] == "pack" {
			if rel == "pack" {
				if !entry.IsDir() {
					return fmt.Errorf("object pack root is not a directory")
				}
				return nil
			}
			if len(parts) != 2 || entry.IsDir() {
				return fmt.Errorf("unsupported packed object entry: %s", rel)
			}
			name := parts[1]
			ext := filepath.Ext(name)
			base := strings.TrimSuffix(name, ext)
			if (ext != ".pack" && ext != ".idx" && ext != ".rev" && ext != ".bitmap") || !strings.HasPrefix(base, "pack-") || !objectHex(strings.TrimPrefix(base, "pack-"), 40) {
				return fmt.Errorf("unsupported packed object entry: %s", rel)
			}
			return nil
		}
		if len(parts) == 1 && entry.IsDir() && objectHex(parts[0], 2) {
			return nil
		}
		if len(parts) == 2 && !entry.IsDir() && objectHex(parts[0], 2) && objectHex(parts[1], 38) {
			return nil
		}
		return fmt.Errorf("unsupported loose object entry: %s", rel)
	})
}
func objectHex(value string, length int) bool {
	return len(value) == length && strings.Trim(value, "0123456789abcdef") == ""
}
func mergeObjects(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Mkdir(target, info.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := directory(target); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := mergeObjects(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return durable.SyncDir(target)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("object is not regular: %s", source)
	}
	existing, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return copyMetadata(source, target)
	}
	if err != nil {
		return err
	}
	if !existing.Mode().IsRegular() {
		return fmt.Errorf("object destination has conflicting type: %s", target)
	}

	if info.Size() != existing.Size() {
		return fmt.Errorf("object contents conflict at %s", target)
	}
	a, err := objectDigest(source)
	if err != nil {
		return err
	}
	b, err := objectDigest(target)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("object contents conflict at %s", target)
	}
	return nil
}
func objectDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return "", err
	}
	return string(hash.Sum(nil)), nil
}
