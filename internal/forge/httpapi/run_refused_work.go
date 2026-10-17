package httpapi

import (
	"fmt"
	"net/http"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/repositoryedit"
)

// RefusedStepWork serves a step attempt's result whose publication was
// refused and which the forge kept, as a Git bundle, as a refused chat save
// is served (ChangeWorkerChats.RefusedBundle).
type RefusedStepWork struct {
	db      database.Queryer
	dataDir string
}

func NewRefusedStepWork(db database.Queryer, dataDir string) RefusedStepWork {
	return RefusedStepWork{db: db, dataDir: dataDir}
}

// Bundle: GET /api/v1/runs/{id}/steps/{step}/attempts/{attempt}/refused, a
// Git bundle of the step attempt's refused result: fetch it to repair the
// commits.
func (h RefusedStepWork) Bundle(w http.ResponseWriter, r *http.Request) {
	run, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	step, err := PathID(r, "step")
	if err != nil {
		WriteError(w, 400, "bad step id")
		return
	}
	attempt, err := PathNumber(r, "attempt")
	if err != nil || attempt < 1 {
		WriteError(w, 400, "bad attempt")
		return
	}
	kept, err := repositoryedit.FindRefusedStepWork(r.Context(), h.db, h.dataDir, run, step, attempt)
	if err != nil {
		WriteFault(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="run-%d-step-%d-a%d.bundle"`, run, step, attempt))
	if err := kept.Bundle(r.Context(), w); err != nil {
		WriteFault(w, err)
	}
}
