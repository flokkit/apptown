package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/operations"

	"github.com/flokkit/apptown/internal/apptown/adrs"
)

type Dashboard struct {
	adrs       *adrs.Service
	operations *operations.Service
}

func NewDashboard(adrs *adrs.Service, ops *operations.Service) Dashboard {
	return Dashboard{adrs: adrs, operations: ops}
}
func (h Dashboard) Dashboard(w http.ResponseWriter, r *http.Request) {
	recentADRs, err := h.adrs.List(r.Context(), adrs.Filter{Recent: true, Limit: 5})
	if err != nil {
		WriteFault(w, err)
		return
	}
	// The operations the caller asked for that failed, or whose outcome is
	// not known, wait on them until they put them away.
	ended, err := h.operations.EndedFor(r.Context(), actor(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, DashboardView{RecentADRs: recentADRs, EndedOperations: ended})
}

// DashboardView supplies recent decisions and the reader’s ended operations.
// Change work and its reader-aware partition are on /activity.
type DashboardView struct {
	RecentADRs []adrs.Record `json:"recentAdrs"`
	// EndedOperations are the caller's own data operations (a copy, a
	// check, a take-down) that failed or whose outcome is not known, and
	// that they have not put away.
	EndedOperations []operations.Operation `json:"endedOperations"`
}
