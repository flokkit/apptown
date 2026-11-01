package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type ChangePreviewState struct {
	db       *database.DB
	previews changePreviewObserver
}

func NewChangePreviewState(db *database.DB, previews changePreviewObserver) ChangePreviewState {
	return ChangePreviewState{db: db, previews: previews}
}

// ChangePreviewState: GET /changes/{id}/preview — what the try-it
// button should say (deploy.go previewState). Cheap enough to poll.
func (h ChangePreviewState) ChangePreviewState(w http.ResponseWriter, r *http.Request) {
	ch, ok := changeFromPath(h.db, w, r)
	if !ok {
		return
	}
	WriteJSON(w, 200, h.previews.State(r.Context(), ch))
}

type changePreviewObserver interface {
	State(context.Context, *workflow.Change) preview.Status
}
