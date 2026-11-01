package httpapi

import (
	"io"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type Attachments struct {
	owner   workflow.Attachments
	dataDir string
}

func NewAttachments(owner workflow.Attachments, dataDir string) Attachments {
	return Attachments{owner: owner, dataDir: dataDir}
}

// UploadAttachment: POST /api/v1/changes/{id}/attachments — multipart
// form, field "file".
func (h Attachments) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
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
	name := hdr.Filename
	b, err := io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
	if err != nil || len(b) > maxAttachmentBytes {
		WriteError(w, 413, "attachment too large (max %d MB)", maxAttachmentBytes>>20)
		return
	}
	added, err := h.owner.Add(r.Context(), id, name, b)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 201, AttachmentAdded{Name: added.Name, Bytes: added.Bytes, SHA256: added.SHA256})
}

// ListAttachments: GET /api/v1/changes/{id}/attachments
// ChangeAttachment is a file pinned to a change for its agents to read.
type ChangeAttachment struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

func (h Attachments) ListAttachments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	files, err := h.owner.List(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []ChangeAttachment{}
	for _, file := range files {
		out = append(out, ChangeAttachment{Name: file.Name, Bytes: file.Bytes})
	}
	WriteJSON(w, 200, out)
}

func (h Attachments) GetAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	path, err := h.owner.File(r.Context(), id, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	ServeUntrustedFile(h.dataDir, w, r, path)
}

func (h Attachments) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.owner.Remove(r.Context(), id, r.PathValue("name")); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

const maxAttachmentBytes = workflow.MaxAttachmentBytes

// AttachmentAdded identifies a file attached to a Change.
type AttachmentAdded struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
