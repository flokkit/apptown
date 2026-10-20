package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/forge/remediation"
)

type ConformanceMaintenance struct {
	fixes   *remediation.Service
	enabled bool
}

func NewConformanceMaintenance(fixes *remediation.Service, enabled bool) ConformanceMaintenance {
	return ConformanceMaintenance{fixes: fixes, enabled: enabled}
}

// ClearConformanceHistory: POST /api/v1/conformance/history/clear —
// the fresh start: delete historical reviews and findings so the instance
// starts clean. Verdicts are immutable records
// by design, so this is the one deliberate act that removes them: every
// review, verdict, AI adjudication, exception, fix record, remediation job
// and recovery posture on the instance, in one transaction, with a receipt
// in the audit log saying who and how many. Refused while a review or a
// remediation job is running. Scanner suppressions on the projects'
// Security pages are a person's decisions about scanner findings, not
// review history, and are kept.
func (h ConformanceMaintenance) ClearConformanceHistory(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		WriteError(w, 404, "deleting conformance history is a development convenience: start apptownd with -features dev-tools (or APPTOWN_FEATURES=dev-tools) to enable it on this AppTown")
		return
	}
	counts, err := h.fixes.ClearHistory(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ConformanceHistoryCleared{OK: true, Removed: counts})
}

// ConformanceHistoryCleared counts what the fresh start removed.
type ConformanceHistoryCleared struct {
	OK      bool                        `json:"ok"`
	Removed *remediation.ClearedHistory `json:"removed"`
}
