package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/progress"
)

type RunProgress struct {
	work  *workflow.Service
	feeds *progress.Feeds
}

func NewRunProgress(work *workflow.Service, feeds *progress.Feeds) RunProgress {
	return RunProgress{work: work, feeds: feeds}
}

// StepLive serves the live feed: ?from=N returns lines after absolute
// offset N plus the new total, so pollers only receive fresh lines.
func (h RunProgress) StepLive(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	from := 0
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			from = n
		}
	}
	work, ctx := h.work, r.Context()
	observed, err := work.Step(ctx, id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	feed, running := h.feeds.Read(id, from)
	if !running {
		lines := []string{}
		reports, err := work.RunArtifacts(ctx, observed.Run.ID, workflow.ArtifactSelection{Kinds: []string{"agent-log"}, Name: fmt.Sprintf("step-%d", id), Latest: true})
		if err != nil {
			WriteFault(w, err)
			return
		}
		if len(reports) > 0 && reports[0].Body != "" {
			lines = strings.Split(reports[0].Body, "\n")
		}
		WriteJSON(w, 200, StepProgress{Running: false, Status: &observed.Step.Status, Lines: lines, Total: len(lines)})
		return
	}
	WriteJSON(w, 200, StepProgress{Running: true, Lines: feed.Lines, Total: feed.Total, ElapsedMs: &feed.ElapsedMs})
}

// StepProgress is a step's live output. A finished step carries its status;
// a running one how long it has run.
type StepProgress struct {
	Running   bool                `json:"running"`
	Status    *workflow.StepState `json:"status,omitempty"`
	Lines     []string            `json:"lines"`
	Total     int                 `json:"total"`
	ElapsedMs *int64              `json:"elapsedMs,omitempty"`
}
