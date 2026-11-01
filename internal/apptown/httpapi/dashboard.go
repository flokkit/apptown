package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/operations"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type Dashboard struct {
	work       *workflow.Service
	adrs       *adrs.Service
	operations *operations.Service
}

func NewDashboard(work *workflow.Service, adrs *adrs.Service, ops *operations.Service) Dashboard {
	return Dashboard{work: work, adrs: adrs, operations: ops}
}
func (h Dashboard) Dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := h.work.Dashboard(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
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
	WriteJSON(w, 200, DashboardView{PendingApprovals: dashboard.PendingApprovals, ActiveRuns: dashboard.ActiveRuns, RecentADRs: recentADRs, EndedOperations: ended})
}

// DashboardView is the instance's front page: approvals waiting, runs under
// way, recent decisions.
type DashboardView struct {
	PendingApprovals []workflow.PendingApproval `json:"pendingApprovals"`
	ActiveRuns       []workflow.ActiveRun       `json:"activeRuns"`
	RecentADRs       []adrs.Record              `json:"recentAdrs"`
	// EndedOperations are the caller's own data operations (a copy, a
	// check, a take-down) that failed or whose outcome is not known, and
	// that they have not put away.
	EndedOperations []operations.Operation `json:"endedOperations"`
}
