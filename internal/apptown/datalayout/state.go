package datalayout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
)

const Version = 2
const journalName = "data-layout-upgrade.json"

// ErrNewerLayout refuses a downgrade before database or application effects.
var ErrNewerLayout = errors.New("the data directory was written by a newer build")

// errOldLayout refuses a data directory from before layout 2. This build does
// not move repositories between layouts; an earlier release does.
var errOldLayout = errors.New("this data directory is from AppTown before 1.0.0-rc.9 (data layout 1): upgrade it with a release from 1.0.0-rc.9 to 1.0.0-rc.11 first, or start with an empty data directory")

// readVersion does not stamp or create anything. A missing marker is a new
// data directory only when no project repository exists in it (Initialize).
func readVersion(root string) (int, error) {
	path := filepath.Join(root, "layout")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, errOldLayout
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("data layout marker is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || version < 1 {
		return 0, fmt.Errorf("unsupported data layout marker %q", strings.TrimSpace(string(raw)))
	}
	if version > Version {
		return 0, fmt.Errorf("%w: layout %d, this build reads %d", ErrNewerLayout, version, Version)
	}
	if version < Version {
		return 0, errOldLayout
	}
	return version, nil
}

// Initialize writes the marker for a data directory that has none and holds
// no project repository. Layouts differ only in where project repositories
// live (workspaces/project-*), so such a directory, new or seeded with files
// such as secrets.env, is already in this layout. It runs before the database
// is opened: a refused older directory is left for its own release to upgrade.
func Initialize(root string) error {
	if _, err := os.Lstat(filepath.Join(root, "layout")); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		_, err := readVersion(root)
		return err
	}
	repositories, err := filepath.Glob(filepath.Join(root, "workspaces", "project-*"))
	if err != nil {
		return err
	}
	if len(repositories) > 0 {
		return errOldLayout
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	_, err = durable.Write(root, "layout", []byte(strconv.Itoa(Version)+"\n"), 0o600, false)
	return err
}

type journal struct {
	Nonce        string    `json:"nonce"`
	Format       int       `json:"format"`
	Instance     string    `json:"instance"`
	Root         string    `json:"root"`
	OriginalRoot string    `json:"originalRoot"`
	Phase        string    `json:"phase"`
	Inventory    inventory `json:"inventory"`
}

func readJournal(root, instance string) (journal, bool, error) {
	var out journal
	path := filepath.Join(root, journalName)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if !info.Mode().IsRegular() {
		return out, false, errors.New("layout journal is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, false, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return out, false, errors.New("layout journal has trailing data")
	}
	if len(out.Nonce) < 26 || len(out.Nonce) > 64 || strings.Trim(out.Nonce, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" || out.Format != 2 || out.Instance != instance || out.Root != root || !filepath.IsAbs(out.OriginalRoot) || filepath.Clean(out.OriginalRoot) != out.OriginalRoot {
		return out, false, errors.New("layout journal does not match this installation")
	}
	switch out.Phase {
	case "references", "verifying", "complete":
	default:
		return out, false, errors.New("unrecognized layout journal phase")
	}
	return out, true, nil
}
func (j journal) save() error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	_, err = durable.Write(j.Root, journalName, append(raw, '\n'), 0o600, false)
	return err
}
func (j *journal) advance(phase string) error {
	j.Phase = phase
	return j.save()
}

// RequireCurrent is the read-only boundary for commands that intentionally do
// not take the daemon's exclusive lock, such as account password reset.
func RequireCurrent(root string) error {
	if _, err := readVersion(root); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, journalName)); err == nil {
		return errors.New("a restored data directory's relocation is pending; start AppTown normally before resetting an account")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
