package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Preview wire fields retain the existing short commit display. Revision comes
// from the prepared tree, even when a historical snapshot has no .git directory.
type changePreviewResponse struct {
	URL string `json:"url"`
	// HostOnly: the URL answers on the AppTown host only, so the page shows
	// it rather than opening it (deployment.Service.PreviewsHostOnly).
	HostOnly bool   `json:"hostOnly,omitempty"`
	Commit   string `json:"commit"`
}

func previewResponse(out preview.Result, hostOnly bool) changePreviewResponse {
	revision := out.Revision
	if len(revision) > 7 {
		revision = revision[:7]
	}
	return changePreviewResponse{URL: out.URL, HostOnly: hostOnly, Commit: revision}
}
func writePreviewError(w http.ResponseWriter, err error, appFailureStatus int) {
	if WriteRefusal(w, err) {
		return
	}
	code := 500
	var execution *preview.ExecutionError
	if errors.As(err, &execution) || errors.Is(err, context.Canceled) {
		code = 422
	}
	var be *docker.BootError
	if errors.As(err, &be) && (be.Phase == docker.PhaseApp || be.Phase == docker.PhaseProbe) {
		code = appFailureStatus
	}
	WriteFaultOr(w, err, code)
}
