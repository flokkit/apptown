package console

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
)

// RelocationRecord is a strictly read live session record. Ended records are
// historical evidence and remain untouched. This never resumes or reaps agents.
type RelocationRecord struct {
	Meta Meta
	File string
	raw  []byte
}

// RelocationRecords returns the complete readable inventory or an error. An
// unreadable record is not an absent session and cannot be silently omitted.
func RelocationRecords(dataDir string) ([]RelocationRecord, error) {
	root := filepath.Join(dataDir, "console")
	if info, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, errors.New("console inventory root is not a directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var records []RelocationRecord
	var unknown []string
	owned := map[string]bool{}
	for _, entry := range entries {
		if entry.Name() == "bin" {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("console inventory contains symlink %s", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), MetaFile)
		ended := false
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			path = filepath.Join(root, entry.Name(), EndedFile)
			ended = true
			info, err = os.Lstat(path)
			if errors.Is(err, fs.ErrNotExist) {
				unknown = append(unknown, entry.Name())
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("console record is not a regular file: %s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var meta Meta
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("reading console %s: %w", entry.Name(), err)
		}
		if meta.ID != entry.Name() || meta.Scope == "" || meta.Checkout == "" || meta.LastUsed.IsZero() {
			return nil, fmt.Errorf("console %s has incomplete or mismatched relocation metadata", entry.Name())
		}
		if meta.OwnsCheckout && filepath.Base(filepath.Dir(meta.Checkout)) == "console" && strings.HasPrefix(filepath.Base(meta.Checkout), "checkout-") {
			owned[filepath.Base(meta.Checkout)] = true
		}
		if !ended {
			records = append(records, RelocationRecord{Meta: meta, File: path, raw: raw})
		}
	}
	for _, name := range unknown {
		if !owned[name] {
			return nil, fmt.Errorf("console directory %s has neither a readable live/ended record nor an identified checkout owner", name)
		}
	}
	return records, nil
}

// RefuseWriter observes the full runtime status. An absent primary container
// does not prove a private daemon or workspace script stopped writing.
func (r RelocationRecord) RefuseWriter(ctx context.Context, agent Agent) error {
	if agent == nil {
		return fmt.Errorf("console %s has no observable runtime", r.Meta.ID)
	}
	status, err := agent.Status(ctx, r.Meta.ID)
	if err != nil {
		return fmt.Errorf("observing console %s before relocation: %w", r.Meta.ID, err)
	}
	if status.Running || status.SidecarRunning || status.ScriptExists || status.Attached {
		return fmt.Errorf("console %s still has a writer; stop its agent, private daemon and scripts before relocating", r.Meta.ID)
	}
	return nil
}

// CheckoutEdit holds only paths and content digests, never session credentials.
// It can be included in a relocation journal and retried after a crash.
type CheckoutEdit struct {
	File         string `json:"file"`
	Before       string `json:"before"`
	After        string `json:"after"`
	BeforeDigest string `json:"beforeDigest"`
	AfterDigest  string `json:"afterDigest"`
}

func (r RelocationRecord) CheckoutEdit(to string) (CheckoutEdit, error) {
	raw, err := rewriteCheckout(r.raw, r.Meta.Checkout, to)
	if err != nil {
		return CheckoutEdit{}, err
	}
	return CheckoutEdit{File: r.File, Before: r.Meta.Checkout, After: to, BeforeDigest: recordDigest(r.raw), AfterDigest: recordDigest(raw)}, nil
}

// Apply changes only checkout, preserving ownership, credentials and unknown
// fields. Exact old/new digests make replay safe; a third state refuses.
func (e CheckoutEdit) Apply() error {
	info, err := os.Lstat(e.File)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("console relocation record is not regular: %s", e.File)
	}
	raw, err := os.ReadFile(e.File)
	if err != nil {
		return err
	}
	got := recordDigest(raw)
	if got == e.AfterDigest {
		return durable.SyncDir(filepath.Dir(e.File))
	}
	if got != e.BeforeDigest {
		return fmt.Errorf("console record changed during relocation: %s", e.File)
	}
	next, err := rewriteCheckout(raw, e.Before, e.After)
	if err != nil {
		return err
	}
	if recordDigest(next) != e.AfterDigest {
		return errors.New("console relocation journal does not match its intended edit")
	}
	_, err = durable.Write(filepath.Dir(e.File), filepath.Base(e.File), next, 0o600, false)
	return err
}
func rewriteCheckout(raw []byte, before, after string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var checkout string
	if err := json.Unmarshal(fields["checkout"], &checkout); err != nil {
		return nil, err
	}
	if checkout != before || after == "" {
		return nil, errors.New("console checkout does not match relocation input")
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		return nil, err
	}
	fields["checkout"] = encoded
	next, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	// json.RawMessage preserves each field's value, including unknown fields.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, next, "", "  "); err != nil {
		return nil, err
	}
	return append(pretty.Bytes(), '\n'), nil
}
func recordDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
