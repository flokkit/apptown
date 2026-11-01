package httpapi

import (
	"context"
	"net/http"
)

type BrowserErrors struct{ reporting browserErrorConfig }

func NewBrowserErrors(reporting browserErrorConfig) BrowserErrors {
	return BrowserErrors{reporting: reporting}
}

// BrowserErrors: GET /api/v1/errors/browser — what AppTown's own web
// UI needs to report its exceptions to the same GlitchTip project: the
// DSN (a DSN is public by design; it identifies, it does not authorise
// reads) and the release. Empty when AppTown project is unattached, and
// the UI then reports nothing.
func (h BrowserErrors) BrowserErrors(w http.ResponseWriter, r *http.Request) {
	dsn, release, ok := h.reporting.BrowserConfig(r.Context())
	if !ok {
		WriteJSON(w, 200, BrowserErrorReporting{})
		return
	}
	WriteJSON(w, 200, BrowserErrorReporting{DSN: dsn, Release: release})
}

type browserErrorConfig interface {
	BrowserConfig(ctx context.Context) (dsn, release string, ok bool)
}

// BrowserErrorReporting is where the web UI reports its own exceptions; both
// fields are absent when AppTown reports nowhere.
type BrowserErrorReporting struct {
	DSN     string `json:"dsn,omitempty"`
	Release string `json:"release,omitempty"`
}
