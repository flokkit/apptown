package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"
)

type SetupStatus struct {
	image           agentImageState
	selection       modelSelection
	openNonLoopback bool
}

func NewSetupStatus(image agentImageState, selection modelSelection, openNonLoopback bool) SetupStatus {
	return SetupStatus{image: image, selection: selection, openNonLoopback: openNonLoopback}
}

// Setup: GET /api/v1/setup — what first-run still needs.
func (h SetupStatus) Setup(w http.ResponseWriter, r *http.Request) {
	img, buildErr := h.image.ImageState()

	// A model setup that cannot be read is not "needs a model".
	pattern, _, _, err := h.selection.Resolve(r.Context(), ids.Nil, "")
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, SetupState{
		NeedsModel:      pattern == "",
		AgentImage:      img,
		AgentImageError: buildErr,
		OpenNonLoopback: h.openNonLoopback,
	})
}

type agentImageState interface{ ImageState() (state, detail string) }
type modelSelection interface {
	Resolve(ctx context.Context, projectID ids.ID, requested string) (pattern, slot, profile string, err error)
}

// SetupState is what first-run setup still needs.
type SetupState struct {
	NeedsModel      bool   `json:"needsModel"`
	AgentImage      string `json:"agentImage"`
	AgentImageError string `json:"agentImageError"`
	OpenNonLoopback bool   `json:"openNonLoopback"`
}
