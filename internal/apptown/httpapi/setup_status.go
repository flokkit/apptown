package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"
)

type SetupStatus struct {
	image           AgentImageState
	selection       ModelSelection
	openNonLoopback bool
}

func NewSetupStatus(image AgentImageState, selection ModelSelection, openNonLoopback bool) SetupStatus {
	return SetupStatus{image: image, selection: selection, openNonLoopback: openNonLoopback}
}

// Setup: GET /api/v1/setup — what first-run still needs.
func (h SetupStatus) Setup(w http.ResponseWriter, r *http.Request) {
	img, buildErr := h.image.ImageState()

	pattern, _, _ := h.selection.Resolve(r.Context(), ids.Nil, "")
	WriteJSON(w, 200, SetupState{
		NeedsModel:      pattern == "",
		AgentImage:      img,
		AgentImageError: buildErr,
		OpenNonLoopback: h.openNonLoopback,
	})
}

type AgentImageState interface{ ImageState() (state, detail string) }
type ModelSelection interface {
	Resolve(ctx context.Context, projectID ids.ID, requested string) (pattern, slot, profile string)
}

// SetupState is what first-run setup still needs.
type SetupState struct {
	NeedsModel      bool   `json:"needsModel"`
	AgentImage      string `json:"agentImage"`
	AgentImageError string `json:"agentImageError"`
	OpenNonLoopback bool   `json:"openNonLoopback"`
}
