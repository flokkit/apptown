package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
)

type ConsolePreview struct {
	sessions   *console.Service
	previews   *preview.Service
	db         *database.DB
	publicHost string
	now        func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewConsolePreview(sessions *console.Service, previews *preview.Service, db *database.DB, publicHost string, now func() time.Time) ConsolePreview {
	return ConsolePreview{sessions: sessions, previews: previews, db: db, publicHost: publicHost, now: now}
}

// ConsolePreview: POST /console/{sid}/preview — the change chat boots
// (or reuses) its own change's preview and learns a URL IT can reach from
// inside its container. Session-token gated; scoped to the console's change.
func (h ConsolePreview) ConsolePreview(w http.ResponseWriter, r *http.Request) {
	s := consoleForTokenRequest(h.sessions, w, r)
	if s == nil {
		return
	}
	if conversation.Of(s).ChangeID.IsZero() {
		WriteError(w, 422, "project chats are read-only — previews belong to a change chat")
		return
	}
	id := conversation.Of(s).ChangeID
	out, err := h.previews.RequestChangePreview(conversation.Context(r.Context(), s), preview.Selection{ChangeID: id})
	if err != nil {
		writePreviewError(w, err, 422)
		return
	}
	port := out.HostPort
	containerURL := fmt.Sprintf("http://host.docker.internal:%d", port)
	switch {
	case h.previews.PreviewsHaveDomain():
		// With a preview domain every chat reaches the preview as any
		// visitor does, at its preview-domain address: a preview on the
		// worker has no port here, and one on this AppTown's Docker publishes
		// its port on the host's loopback, which the chat cannot reach.
		containerURL = out.URL
	case h.publicHost != "":
		containerURL = fmt.Sprintf("http://%s:%d", h.publicHost, port)
	}
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, s.Actor+" via console/"+s.ID, out.ProjectID, "change.preview", fmt.Sprintf("%d", id), map[string]any{"url": out.URL, "from": "console"})
	WriteJSON(w, 200, ConsolePreviewAddress{URL: out.URL, ContainerURL: containerURL, Port: port})
}

// ConsolePreviewAddress is where a chat's preview answers, from outside and
// from inside its container.
type ConsolePreviewAddress struct {
	URL          string `json:"url"`
	ContainerURL string `json:"containerUrl"`
	Port         int    `json:"port"`
}
