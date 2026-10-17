package httpapi

import (
	"net/http"
	"strconv"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/jobs"
)

type Jobs struct{ db *database.DB }

func NewJobs(db *database.DB) Jobs { return Jobs{db: db} }

// ListJobs: GET /jobs[?limit=] — the open jobs and the recent ones,
// newest first.
func (h Jobs) ListJobs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	out, err := jobs.List(r.Context(), h.db, limit)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, JobList{Jobs: out})
}

// JobList is the open and recent background jobs, newest first.
type JobList struct {
	Jobs []jobs.Job `json:"jobs"`
}
