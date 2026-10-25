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

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/durable"
)

const Version = 2
const journalName = "data-layout-upgrade.json"

// ErrNewerLayout refuses a downgrade before database or application effects.
var ErrNewerLayout = errors.New("the data directory was written by a newer build")

// ReadVersion does not stamp or create anything. Missing is the supported
// addressed layout; only a completed upgrade is permitted to write Version.
func ReadVersion(root string) (int, error) {
	path := filepath.Join(root, "layout")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 1, nil
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
	return version, nil
}

type journal struct {
	Nonce        string                              `json:"nonce"`
	Format       int                                 `json:"format"`
	Instance     string                              `json:"instance"`
	Root         string                              `json:"root"`
	OriginalRoot string                              `json:"originalRoot"`
	FromVersion  int                                 `json:"fromVersion"`
	Phase        string                              `json:"phase"`
	Inventory    inventory                           `json:"inventory"`
	Exchanges    []repositorylayout.MetadataExchange `json:"exchanges,omitempty"`
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
	if len(out.Nonce) < 26 || len(out.Nonce) > 64 || strings.Trim(out.Nonce, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" || out.Format != 1 || out.Instance != instance || out.Root != root || !filepath.IsAbs(out.OriginalRoot) || filepath.Clean(out.OriginalRoot) != out.OriginalRoot || (out.FromVersion != 1 && out.FromVersion != 2) {
		return out, false, errors.New("layout journal does not match this installation")
	}
	switch out.Phase {
	case "planned", "staging", "moving", "metadata", "references", "verifying", "complete":
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
	version, err := ReadVersion(root)
	if err != nil {
		return err
	}
	if version != Version {
		return errors.New("data layout upgrade is pending; start AppTown normally to complete it before resetting an account")
	}
	if _, err := os.Lstat(filepath.Join(root, journalName)); err == nil {
		return errors.New("data layout completion is pending; start AppTown normally before resetting an account")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
