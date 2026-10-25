// Package repositorylayout owns the offline, journaled move from addressed
// project hubs to UUID hubs. It does not select an application repository.
package repositorylayout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/ids"
)

const journalFile = "repository-layout.json"
const stagingName = ".repository-layout-stage"

// Mapping comes from the project owner's complete offline inventory. Missing
// permits an absent repository only when durable lifecycle evidence allows it.
type Mapping struct {
	ID         ids.ID `json:"id"`
	Slug       string `json:"slug"`
	Missing    bool   `json:"missing"`
	SourceUUID bool   `json:"sourceUUID"`
}

type move struct {
	Mapping
	Digest string `json:"digest"`
	Absent bool   `json:"absent"`
	Phase  string `json:"phase"`
}

type record struct {
	Version  int    `json:"version"`
	Instance string `json:"instance"`
	Root     string `json:"root"`
	Moves    []move `json:"moves"`
}

// Moves is a durable mapping, held while the daemon's exclusive lock excludes
// all consumers. Callers establish writer quiescence before Prepare and retain
// that exclusion through file/SQL repairs and layout stamping.
type Moves struct {
	root   string
	record record
}

func paths(root string, m Mapping) (source, staging, target string) {
	name := m.Slug
	if m.SourceUUID {
		name = m.ID.String()
	}
	return filepath.Join(root, "workspaces", "project-"+name), filepath.Join(root, stagingName, m.ID.String()), filepath.Join(root, "workspaces", "project-"+m.ID.String())
}

// Check validates every source and destination without writing migration intent.
func Check(root, instance string, mappings []Mapping) error {
	_, err := prepare(root, instance, mappings, false)
	return err
}

// Prepare checks the entire namespace before persisting intent. An existing
// journal is resumed only for exactly the same installation and owner mapping.
func Prepare(root, instance string, mappings []Mapping) (*Moves, error) {
	return prepare(root, instance, mappings, true)
}
func prepare(root, instance string, mappings []Mapping, persist bool) (*Moves, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if instance == "" {
		return nil, errors.New("repository relocation needs installation identity")
	}
	mappings = slices.Clone(mappings)
	slices.SortFunc(mappings, func(a, b Mapping) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	seenID, seenSlug := map[ids.ID]bool{}, map[string]bool{}
	for _, m := range mappings {
		if m.ID.IsZero() || !safeSlug(m.Slug) || seenID[m.ID] || seenSlug[m.Slug] {
			return nil, fmt.Errorf("invalid or overlapping repository owner %s/%q", m.ID, m.Slug)
		}
		seenID[m.ID], seenSlug[m.Slug] = true, true
	}
	if err := directory(root); err != nil {
		return nil, err
	}
	if err := optionalDirectory(filepath.Join(root, "workspaces")); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(filepath.Join(root, journalFile)); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("repository move journal is not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, "workspaces")); err == nil {
		if err := sameDevice(root, filepath.Join(root, "workspaces")); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, journalFile))
	if err == nil {
		var r record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("reading repository move journal: %w", err)
		}
		if r.Version != 1 || r.Instance != instance || r.Root != root || len(r.Moves) != len(mappings) {
			return nil, errors.New("repository move journal belongs to a different installation or mapping")
		}
		for i, m := range mappings {
			if r.Moves[i].Mapping != m {
				return nil, errors.New("repository owners changed while relocation was pending")
			}
			switch r.Moves[i].Phase {
			case "planned", "staging", "staged", "installing", "installed":
			default:
				return nil, errors.New("invalid repository move phase")
			}
			if !r.Moves[i].Absent && len(r.Moves[i].Digest) != 64 {
				return nil, errors.New("invalid repository move digest")
			}
		}
		if err := optionalDirectory(filepath.Join(root, stagingName)); err != nil {
			return nil, err
		}
		return &Moves{root: root, record: r}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// An unjournaled staging directory cannot be adopted, even if empty.
	if _, err := os.Lstat(filepath.Join(root, stagingName)); err == nil {
		return nil, errors.New("repository staging exists without its journal; preserve it for repair")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	r := record{Version: 1, Instance: instance, Root: root}
	sources := map[string]bool{}
	for _, m := range mappings {
		source, _, _ := paths(root, m)
		sources[source] = true
	}
	for _, m := range mappings {
		source, _, target := paths(root, m)
		_, sourceErr := os.Lstat(source)
		absent := errors.Is(sourceErr, fs.ErrNotExist)
		var digest string
		if !absent {
			if sourceErr != nil {
				return nil, sourceErr
			}
			if err := sameDevice(root, source); err != nil {
				return nil, err
			}
			digest, err = treeDigest(source)
		} else {
			err = nil
		}
		if absent && !m.Missing {
			return nil, fmt.Errorf("initialized project %s has no repository at %s", m.ID, source)
		}
		if err != nil {
			return nil, fmt.Errorf("inspecting project %s: %w", m.ID, err)
		}
		if _, err := os.Lstat(target); err == nil && !sources[target] {
			return nil, fmt.Errorf("project %s destination is already occupied: %s", m.ID, target)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		r.Moves = append(r.Moves, move{Mapping: m, Digest: digest, Absent: absent, Phase: "planned"})
	}
	out := &Moves{root: root, record: r}
	if persist {
		if err := out.save(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Install stages every source before installing any destination, so an old
// address which is another project's UUID cannot cause cross-project adoption.
// Every interrupted rename is reconciled against the recorded content digest.
func (m *Moves) Install() error {
	if err := os.Mkdir(filepath.Join(m.root, stagingName), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := directory(filepath.Join(m.root, stagingName)); err != nil {
		return err
	}
	if err := durable.SyncDir(m.root); err != nil {
		return err
	}
	for i := range m.record.Moves {
		v := &m.record.Moves[i]
		source, stage, target := paths(m.root, v.Mapping)
		if v.Absent || source == target {
			if v.Phase == "planned" {
				v.Phase = "installed"
				if err := m.save(); err != nil {
					return err
				}
			}
			continue
		}
		switch v.Phase {
		case "planned", "staging":
			if v.Phase == "planned" {
				v.Phase = "staging"
				if err := m.save(); err != nil {
					return err
				}
			}
			if err := moveChecked(source, stage, v.Digest); err != nil {
				return fmt.Errorf("staging project %s: %w", v.ID, err)
			}
			v.Phase = "staged"
			if err := m.save(); err != nil {
				return err
			}
		}
	}
	for i := range m.record.Moves {
		v := &m.record.Moves[i]
		source, stage, target := paths(m.root, v.Mapping)
		if v.Absent {
			// Another owner's old source may legitimately have occupied this target;
			// all sources have now been staged and it must be absent.
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("absent project %s has an unexpected destination: %v", v.ID, err)
			}
			continue
		}
		if source == target || v.Phase == "installed" {
			if err := verify(target, v.Digest); err != nil {
				return err
			}
			continue
		}
		if v.Phase != "staged" && v.Phase != "installing" {
			return errors.New("repository was not staged before installation")
		}
		v.Phase = "installing"
		if err := m.save(); err != nil {
			return err
		}
		if err := moveChecked(stage, target, v.Digest); err != nil {
			return fmt.Errorf("installing project %s: %w", v.ID, err)
		}
		v.Phase = "installed"
		if err := m.save(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Moves) save() error {
	raw, err := json.MarshalIndent(m.record, "", "  ")
	if err != nil {
		return err
	}
	_, err = durable.Write(m.root, journalFile, append(raw, '\n'), 0o600, false)
	return err
}

func moveChecked(from, to, digest string) error {
	_, fromErr := os.Lstat(from)
	_, toErr := os.Lstat(to)
	if errors.Is(fromErr, fs.ErrNotExist) && toErr == nil {
		if err := verify(to, digest); err != nil {
			return err
		}
		return durable.SyncDir(filepath.Dir(to))
	}
	if fromErr != nil {
		return fromErr
	}
	if !errors.Is(toErr, fs.ErrNotExist) {
		return fmt.Errorf("destination is not absent: %s (%v)", to, toErr)
	}
	if err := verify(from, digest); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return errors.Join(durable.SyncDir(filepath.Dir(from)), durable.SyncDir(filepath.Dir(to)))
}

func verify(path, want string) error {
	got, err := treeDigest(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("repository content changed at %s; relocation is stopped", path)
	}
	return nil
}

func directory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("repository layout path is not a directory: %s", path)
	}
	return nil
}
func optionalDirectory(path string) error {
	err := directory(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
func safeSlug(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// treeDigest reads without following symlinks. Working-tree symlinks retain
// their link text; a symlink at the repository root is never traversed.
func treeDigest(root string) (string, error) {
	if err := directory(root); err != nil {
		return "", err
	}
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:%s:%d:", len(rel), rel, info.Mode())
		switch {
		case info.IsDir():
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%d:%s", len(target), target)
		case info.Mode().IsRegular():
			fmt.Fprintf(h, "%d:", info.Size())
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(h, f)
			return errors.Join(copyErr, f.Close())
		default:
			return fmt.Errorf("unsupported repository file %s", path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameDevice(root, path string) error {
	a, err := os.Lstat(root)
	if err != nil {
		return err
	}
	b, err := os.Lstat(path)
	if err != nil {
		return err
	}
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	if !xok || !yok || x.Dev != y.Dev {
		return fmt.Errorf("repository relocation requires one filesystem: %s", path)
	}
	return nil
}

// Reference names the original installed path and its final UUID path. An
// already UUID-based archive still needs root relocation but no hub rename.
func (m Mapping) Reference(oldRoot, newRoot string) Reference {
	source, _, _ := paths(oldRoot, m)
	_, _, target := paths(newRoot, m)
	return Reference{Owner: m.ID, Before: source, After: target}
}

// Source is the repository location before this recorded move, within the
// current data root (which may differ from an archive's original root).
func (m Mapping) Source(root string) string { source, _, _ := paths(root, m); return source }

// ClearCompleted removes only an installed move journal and its empty staging
// directory. The coordinator calls this after domain verification and stamping;
// repository digests may now differ because authorized metadata repairs ran.
func ClearCompleted(root, instance string, mappings []Mapping) error {
	if _, err := os.Lstat(filepath.Join(root, journalFile)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	moves, err := prepare(root, instance, mappings, false)
	if err != nil {
		return err
	}
	for _, move := range moves.record.Moves {
		if move.Phase != "installed" {
			return errors.New("cannot clear unfinished repository moves")
		}
	}
	staging := filepath.Join(root, stagingName)
	if err := os.Remove(staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(root, journalFile)); err != nil {
		return err
	}
	return durable.SyncDir(root)
}
