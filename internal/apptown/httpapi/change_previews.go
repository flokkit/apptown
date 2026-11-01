package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type ChangePreviews struct {
	previews *preview.Service
	db       *database.DB
	now      func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewChangePreviews(previews *preview.Service, db *database.DB, now func() time.Time) ChangePreviews {
	return ChangePreviews{previews: previews, db: db, now: now}
}

// ChangePreview: POST /changes/{id}/preview — a preview straight from
// the change's WORKTREE, for chat-driven changes where no run has built one
// (someone working in a change could not preview it). The helper's
// uncommitted edits are exactly what you want to try. Reaped when the
// change leaves in_progress.
func (h ChangePreviews) ChangePreview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	out, err := h.previews.RequestChangePreview(r.Context(), preview.Selection{ChangeID: id})
	if err != nil {
		writePreviewError(w, err, 500)
		return
	}
	response := previewResponse(out, h.previews.PreviewsHostOnly())
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, actor(r), out.ProjectID, "change.preview", fmt.Sprintf("%d", id), map[string]any{"url": out.URL, "commit": response.Commit})
	WriteJSON(w, 200, response)
}

// ChangePreviewRestart: POST /changes/{id}/preview/restart — throw the
// running preview away and boot again from the worktree as it is now.
func (h ChangePreviews) ChangePreviewRestart(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	out, err := h.previews.RequestChangePreview(r.Context(), preview.Selection{ChangeID: id, Restart: true})
	if err != nil {
		writePreviewError(w, err, 422)
		return
	}
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, actor(r), out.ProjectID, "change.preview", fmt.Sprintf("%d", id), map[string]any{"url": out.URL, "restart": true})
	WriteJSON(w, 200, previewResponse(out, h.previews.PreviewsHostOnly()))
}
