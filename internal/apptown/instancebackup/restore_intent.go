package instancebackup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
)

const restoreIntentFile = ".apptown-restore-intent.json"

// PendingRestore is a completely loaded archive whose offline layout repair
// and AfterRestore bookkeeping must finish before ordinary startup.
type PendingRestore struct {
	Manifest      *Manifest
	LayoutVersion int
	digest        string
}

type restoreIntent struct {
	Format      int    `json:"format"`
	Destination string `json:"destination"`
	Phase       string `json:"phase"`
	// A string preserves accepted bytes, unknown fields and absent provenance.
	ManifestJSON   string `json:"manifestJson"`
	ManifestDigest string `json:"manifestDigest"`
	LayoutVersion  int    `json:"layoutVersion"`
}

// ReadPendingRestore is read-only and belongs before database/schema effects.
// An interrupted extraction or load is not a normal installed database.
func ReadPendingRestore(dataDir string) (*PendingRestore, error) {
	record, digest, found, err := readRestoreIntent(dataDir)
	if err != nil || !found {
		return nil, err
	}
	if record.Phase != "loaded" {
		return nil, errors.New("this restore did not finish extracting and loading its database; restore the original archive into a fresh empty data directory and empty built-in database, or clear this failed fresh-host restore as the backup guide describes")
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(record.ManifestJSON), &manifest); err != nil {
		return nil, err
	}
	return &PendingRestore{Manifest: &manifest, LayoutVersion: record.LayoutVersion, digest: digest}, nil
}

// CompleteRestore requires the caller's exclusive daemon/data ownership lock
// and successful layout repair plus AfterRestore. The digest detects changed
// evidence; it is not a lock-free compare-and-delete primitive.
func CompleteRestore(dataDir string, pending *PendingRestore) error {
	if pending == nil || pending.digest == "" {
		return errors.New("restore completion needs its loaded continuation")
	}
	record, digest, found, err := readRestoreIntent(dataDir)
	if err != nil {
		return err
	}
	if !found || record.Phase != "loaded" || digest != pending.digest {
		return errors.New("restore intent changed before completion")
	}
	if err := os.Remove(filepath.Join(record.Destination, restoreIntentFile)); err != nil {
		return err
	}
	return durable.SyncDir(record.Destination)
}

func readRestoreIntent(dataDir string) (restoreIntent, string, bool, error) {
	var record restoreIntent
	destination, err := filepath.Abs(dataDir)
	if err != nil {
		return record, "", false, err
	}
	control := filepath.Join(destination, restoreIntentFile)
	info, err := os.Lstat(control)
	if errors.Is(err, fs.ErrNotExist) {
		return record, "", false, nil
	}
	if err != nil {
		return record, "", false, err
	}
	if !info.Mode().IsRegular() {
		return record, "", false, errors.New("restore intent is not a regular file")
	}
	raw, err := os.ReadFile(control)
	if err != nil {
		return record, "", false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, "", false, fmt.Errorf("reading restore intent: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return record, "", false, errors.New("restore intent has trailing data")
	}
	if record.Format != 1 || record.Destination != destination || (record.Phase != "incomplete" && record.Phase != "loaded") || record.ManifestDigest != restoreDigest([]byte(record.ManifestJSON)) || record.LayoutVersion < 0 || (record.Phase == "loaded" && record.LayoutVersion < 1) {
		return record, "", false, errors.New("restore intent is invalid or belongs to another destination")
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(record.ManifestJSON), &manifest); err != nil {
		return record, "", false, err
	}
	if !filepath.IsAbs(manifest.DataDir) || filepath.Clean(manifest.DataDir) != manifest.DataDir {
		return record, "", false, errors.New("restore intent has no absolute original data directory")
	}
	return record, restoreDigest(raw), true, nil
}
func beginRestoreIntent(dataDir string, manifestRaw []byte) error {
	destination, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return err
	}
	if !filepath.IsAbs(manifest.DataDir) || filepath.Clean(manifest.DataDir) != manifest.DataDir {
		return errors.New("archive has no absolute original data directory")
	}
	record := restoreIntent{Format: 1, Destination: destination, Phase: "incomplete", ManifestJSON: string(manifestRaw), ManifestDigest: restoreDigest(manifestRaw)}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	existed, err := durable.Write(destination, restoreIntentFile, append(raw, '\n'), 0o600, true)
	if err != nil {
		return err
	}
	if existed {
		return errors.New("a restore intent already exists; preserve it for repair")
	}
	return nil
}
func recordRestoreLayout(dataDir string, maximum int) error {
	record, digest, found, err := readRestoreIntent(dataDir)
	if err != nil {
		return err
	}
	if !found || record.Phase != "incomplete" {
		return errors.New("source layout needs an incomplete restore intent")
	}
	if maximum < 1 {
		return errors.New("restore needs the supported data layout maximum")
	}
	layout := 1
	marker := filepath.Join(dataDir, "layout")
	info, err := os.Lstat(marker)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("archive layout marker is not a regular file")
		}
		raw, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		layout, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || layout < 1 {
			return errors.New("archive layout marker is invalid")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if layout > maximum {
		return fmt.Errorf("archive data layout %d is newer than this build's %d", layout, maximum)
	}
	record.LayoutVersion = layout
	return replaceRestoreIntent(record, digest)
}
func markRestoreLoaded(dataDir string) error {
	record, digest, found, err := readRestoreIntent(dataDir)
	if err != nil {
		return err
	}
	if !found || record.Phase != "incomplete" || record.LayoutVersion < 1 {
		return errors.New("loaded restore needs its original layout and incomplete intent")
	}
	record.Phase = "loaded"
	return replaceRestoreIntent(record, digest)
}
func replaceRestoreIntent(record restoreIntent, expected string) error {
	_, digest, found, err := readRestoreIntent(record.Destination)
	if err != nil {
		return err
	}
	if !found || digest != expected {
		return errors.New("restore intent changed during loading")
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	_, err = durable.Write(record.Destination, restoreIntentFile, append(raw, '\n'), 0o600, false)
	return err
}
func restoreDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func refusePendingRestore(dataDir string) error {
	pending, err := ReadPendingRestore(dataDir)
	if err != nil {
		return err
	}
	if pending != nil {
		return errors.New("finish this restored installation before taking another archive")
	}
	return nil
}
func refuseRestoreControlEntry(hdr *tar.Header, dataDir string) error {
	rel := path.Clean(strings.TrimPrefix(hdr.Name, dataPrefix))
	reserved := func(name string) bool {
		return name == restoreIntentFile || strings.HasPrefix(name, restoreIntentFile+"/")
	}
	if reserved(rel) {
		return errors.New("archive contains reserved restore control path")
	}
	if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
		target := path.Clean(path.Join(path.Dir(rel), hdr.Linkname))
		if path.IsAbs(hdr.Linkname) {
			root, err := filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, hdr.Linkname)
			if err != nil {
				return err
			}
			target = filepath.ToSlash(relative)
		}
		if reserved(target) {
			return errors.New("archive link targets reserved restore control path")
		}
	}
	return nil
}

// Extraction closes files but does not promise they reached disk. Flush the
// extracted tree before database creation, so a durable loaded continuation
// cannot outlive the files it attests were restored. WalkDir never follows links.
func syncRestoredFiles(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("restored entry is not a regular file: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		return errors.Join(file.Sync(), file.Close())
	})
	if err != nil {
		return err
	}
	for index := len(dirs) - 1; index >= 0; index-- {
		if err := durable.SyncDir(dirs[index]); err != nil {
			return err
		}
	}
	return nil
}
