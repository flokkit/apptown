package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Attachments owns a Change's human-supplied files and their removal receipts.
// Construction binds storage and authority; each operation checks its caller.
type Attachments struct {
	changes Changes
	files   GitWorkspace
}

func NewAttachments(db *database.DB, authority access.Authorizer, files GitWorkspace, now func() time.Time) Attachments {
	return Attachments{changes: NewChanges(db, authority, now), files: files}
}

const MaxAttachmentBytes = 20 << 20

type AttachmentAdded struct {
	Name   string
	Bytes  int
	SHA256 string
}
type Attachment struct {
	Name  string
	Bytes int64
}

func attachmentName(name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
		return "", fault.New(fault.Invalid, "bad attachment name")
	}
	return name, nil
}

func (a Attachments) admit(ctx context.Context, id ids.ID, action commandAction) (ids.ID, commandActor, error) {
	project, who, err := a.changes.scope(ctx, id, action)
	if err != nil {
		return ids.Nil, commandActor{}, err
	}
	if a.files.DataDir == "" {
		return ids.Nil, commandActor{}, fmt.Errorf("attachment storage is not configured")
	}
	return project, who, nil
}

// Add never replaces an attachment. Workspace copying and the addition receipt
// remain best effort after the canonical file is written.
func (a Attachments) Add(ctx context.Context, id ids.ID, name string, body []byte) (AttachmentAdded, error) {
	project, who, err := a.admit(ctx, id, actionAttachChange)
	if err != nil {
		return AttachmentAdded{}, err
	}
	ch, err := readChangeInProject(ctx, a.changes.db, id, project)
	if err != nil {
		return AttachmentAdded{}, err
	}
	if ch.State == ChangeShipped || ch.State == ChangeAbandoned {
		return AttachmentAdded{}, fault.New(fault.Conflict, fmt.Sprintf("change #%d is %s", id, ch.State))
	}
	name, err = attachmentName(name)
	if err != nil {
		return AttachmentAdded{}, err
	}
	if len(body) > MaxAttachmentBytes {
		return AttachmentAdded{}, fault.New(fault.Invalid, "attachment too large")
	}
	dir := a.files.AttachmentDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return AttachmentAdded{}, err
	}
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, fs.ErrExist) {
		return AttachmentAdded{}, fault.New(fault.Conflict, fmt.Sprintf("this change already has an attachment named %s; remove it first", name))
	}
	if err != nil {
		return AttachmentAdded{}, err
	}
	if _, err := out.Write(body); err != nil {
		_ = out.Close()
		return AttachmentAdded{}, err
	}
	if err := out.Close(); err != nil {
		return AttachmentAdded{}, err
	}
	if err := a.files.SyncAttachments(ctx, id, a.files.Paths().ChangeWorkspace(id)); err != nil {
		log.Printf("change %d: copying attachments: %v", id, err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	audit.At(a.changes.now()).BestEffort(ctx, a.changes.db, who.Receipt, project, "change.attachment-added", fmt.Sprintf("change %d: %s", id, name), map[string]any{"sha256": digest, "bytes": len(body)})
	return AttachmentAdded{name, len(body), digest}, nil
}

func (a Attachments) List(ctx context.Context, id ids.ID) ([]Attachment, error) {
	if _, _, err := a.admit(ctx, id, actionReadChange); err != nil {
		return nil, err
	}
	return a.files.Attachments(id)
}

// File admits the name before the transport's confined untrusted-file serving.
func (a Attachments) File(ctx context.Context, id ids.ID, name string) (string, error) {
	if _, _, err := a.admit(ctx, id, actionReadChange); err != nil {
		return "", err
	}
	name, err := attachmentName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(a.files.AttachmentDir(id), name), nil
}

// Remove records intent before either copy is removed. A partial filesystem
// failure or an unrecorded outcome is an error even if some bytes are gone.
func (a Attachments) Remove(ctx context.Context, id ids.ID, name string) error {
	project, who, err := a.admit(ctx, id, actionAttachChange)
	if err != nil {
		return err
	}
	name, err = attachmentName(name)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("change %d: %s", id, name)
	if err := audit.At(a.changes.now()).Record(ctx, a.changes.db, who.Receipt, project, "change.attachment-remove-requested", subject, nil); err != nil {
		return fault.Wrap(fault.Unavailable, fmt.Errorf("not removed: the request could not be recorded: %w", err))
	}
	settle := context.WithoutCancel(ctx)
	for _, path := range []string{filepath.Join(a.files.AttachmentDir(id), name), filepath.Join(a.files.Paths().ChangeWorkspace(id), ".apptown", "attachments", name)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			why := err.Error()
			if recErr := audit.At(a.changes.now()).Record(settle, a.changes.db, who.Receipt, project, "change.attachment-remove-failed", subject, map[string]any{"error": why}); recErr != nil {
				why += "; and recording that it failed: " + recErr.Error()
			}
			return fmt.Errorf("not removed: %s", why)
		}
	}
	if err := audit.At(a.changes.now()).Record(settle, a.changes.db, who.Receipt, project, "change.attachment-removed", subject, nil); err != nil {
		return fmt.Errorf("%s was removed, but its outcome was not recorded: %w", name, err)
	}
	return nil
}
