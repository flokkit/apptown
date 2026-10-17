package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"
)

type Attachments struct {
	db      *database.DB
	dataDir string
	now     func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
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
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
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
	_ = os.Remove(filepath.Join((workflow.GitWorkspace{DataDir: h.dataDir}).AttachmentDir(ch.ID), name))
	wt := filepath.Join(h.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	_ = os.Remove(filepath.Join(wt, ".forge", "attachments", name))
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "change.attachment-removed", fmt.Sprintf("change %d: %s", ch.ID, name), nil)
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
