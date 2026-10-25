package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type Attachments struct {
	db      *database.DB
	dataDir string
	now     func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewAttachments(db *database.DB, dataDir string, now func() time.Time) Attachments {
	return Attachments{db: db, dataDir: dataDir, now: now}
}

// UploadAttachment: POST /api/v1/changes/{id}/attachments — multipart
// form, field "file".
func (h Attachments) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	if ch.State == workflow.ChangeShipped || ch.State == workflow.ChangeAbandoned {
		WriteError(w, 409, "change #%d is %s", ch.ID, ch.State)
		return
	}
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		WriteError(w, 400, "multipart form required: %v", err)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		WriteError(w, 400, "file field required")
		return
	}
	defer f.Close()
	name := safeAttachmentName(hdr.Filename)
	if name == "" {
		WriteError(w, 400, "bad filename")
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
	if err != nil || len(b) > maxAttachmentBytes {
		WriteError(w, 413, "attachment too large (max %d MB)", maxAttachmentBytes>>20)
		return
	}
	dir := (workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentDir(ch.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		WriteFault(w, err)
		return
	}
	// An attachment is never replaced in place: removing it is the one
	// way its bytes go, and that is recorded.
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		WriteError(w, 409, "this change already has an attachment named %s; remove it first", name)
		return
	}
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, err := out.Write(b); err != nil {
		_ = out.Close()
		WriteFault(w, err)
		return
	}
	if err := out.Close(); err != nil {
		WriteFault(w, err)
		return
	}
	if err := (workflow.GitWorkspace{DataDir: h.dataDir}).SyncAttachments(r.Context(), ch.ID, (workflow.Layout{DataDir: h.dataDir}).ChangeWorkspace(ch.ID)); err != nil {
		log.Printf("change %d: copying attachments: %v", ch.ID, err)
	}
	sum := sha256.Sum256(b)
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "change.attachment-added", fmt.Sprintf("change %d: %s", ch.ID, name),
		map[string]any{"sha256": hex.EncodeToString(sum[:]), "bytes": len(b)})
	WriteJSON(w, 201, AttachmentAdded{Name: name, Bytes: len(b), SHA256: hex.EncodeToString(sum[:])})
}

// ListAttachments: GET /api/v1/changes/{id}/attachments
// ChangeAttachment is a file pinned to a change for its agents to read.
type ChangeAttachment struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

func (h Attachments) ListAttachments(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	out := []ChangeAttachment{}
	for _, n := range (workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentNames(ch.ID) {
		fi, err := os.Stat(filepath.Join((workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentDir(ch.ID), n))
		if err == nil {
			out = append(out, ChangeAttachment{Name: n, Bytes: fi.Size()})
		}
	}
	WriteJSON(w, 200, out)
}

// GetAttachment: GET /api/v1/changes/{id}/attachments/{name}
func (h Attachments) GetAttachment(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	name := safeAttachmentName(r.PathValue("name"))
	if name == "" {
		WriteError(w, 400, "bad name")
		return
	}
	ServeUntrustedFile(h.dataDir, w, r, filepath.Join((workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentDir(ch.ID), name))
}

// DeleteAttachment: DELETE /api/v1/changes/{id}/attachments/{name}
func (h Attachments) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	name := safeAttachmentName(r.PathValue("name"))
	if name == "" {
		WriteError(w, 400, "bad name")
		return
	}
	// Removing destroys work supplied to the change: it is requested on the
	// record before the files go, and its outcome recorded after.
	subject := fmt.Sprintf("change %d: %s", ch.ID, name)
	if err := audit.At(clock.Or(h.now)()).Record(r.Context(), h.db, Actor(r), p.ID, "change.attachment-remove-requested", subject, nil); err != nil {
		WriteError(w, 503, "not removed: the request could not be recorded: %v", err)
		return
	}
	settle := context.WithoutCancel(r.Context())
	wt := (workflow.Layout{DataDir: h.dataDir}).ChangeWorkspace(ch.ID)
	for _, path := range []string{filepath.Join((workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentDir(ch.ID), name), filepath.Join(wt, ".apptown", "attachments", name)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			why := err.Error()
			if recErr := audit.At(clock.Or(h.now)()).Record(settle, h.db, Actor(r), p.ID, "change.attachment-remove-failed", subject, map[string]any{"error": why}); recErr != nil {
				why += "; and recording that it failed: " + recErr.Error()
			}
			WriteError(w, 500, "not removed: %s", why)
			return
		}
	}
	if err := audit.At(clock.Or(h.now)()).Record(settle, h.db, Actor(r), p.ID, "change.attachment-removed", subject, nil); err != nil {
		WriteError(w, 500, "%s was removed, but its outcome was not recorded: %v", name, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

const maxAttachmentBytes = 20 << 20

func safeAttachmentName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return ""
	}
	return name
}

// AttachmentAdded identifies a file attached to a Change.
type AttachmentAdded struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
