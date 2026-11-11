package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
	"github.com/flokkit/apptown/internal/version"
)

type Executor struct {
	work     *workflow.Service
	previews *preview.Service
	observer interface {
		Snapshot(context.Context) (maintenance.WorkerSnapshot, error)
	}
	address string
	// hostTarget is the target whose Docker runs the work, when AppTown
	// reads that host: the built-in Docker host while no worker is set.
	hostTarget string
}

func NewExecutor(observer interface {
	Snapshot(context.Context) (maintenance.WorkerSnapshot, error)
}, address, hostTarget string, previews *preview.Service, work *workflow.Service) Executor {
	return Executor{observer: observer, address: address, hostTarget: hostTarget, previews: previews, work: work}
}

type ExecutorPreview struct {
	preview.ExecutionPreview
	ChangeID ids.ID `json:"changeId,omitzero"`
}

type ExecutorView struct {
	Previews        []ExecutorPreview `json:"previews"`
	PreviewsUnknown bool              `json:"previewsUnknown"`
	Configured      bool              `json:"configured"`
	Address         string            `json:"address"`
	State           string            `json:"state"`
	CheckedAt       stamp.Time        `json:"checkedAt,omitzero"`
	Version         string            `json:"version"`
	AppTownVersion  string            `json:"appTownVersion"`
	VersionMismatch bool              `json:"versionMismatch"`
	Stale           bool              `json:"stale"`
	Failure         string            `json:"failure,omitempty"`
	// HostTarget names the deployment target whose Docker runs the work
	// when AppTown reads that host's CPU and memory (its capacity); absent
	// with a worker, whose machine's CPU and memory AppTown does not read.
	HostTarget string `json:"hostTarget,omitempty"`
}

// Get reads the same stored observation used for worker attention. It makes no worker calls.
func (h Executor) Get(w http.ResponseWriter, r *http.Request) {
	s, err := h.observer.Snapshot(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	v := ExecutorView{Configured: s.Configured, Address: h.address, State: "unknown", CheckedAt: stamp.Of(s.AttemptedAt), Version: s.Version, AppTownVersion: version.String(), Stale: s.Stale, Failure: s.Failure, HostTarget: h.hostTarget}
	if !s.Unknown && !s.Stale && s.Availability == execution.WorkerAvailable {
		v.State = "reachable"
	}
	if s.Unknown || s.Stale {
		v.Version = ""
	}
	if h.previews != nil {
		previews, err := h.previews.ExecutionPreviews(r.Context(), s.Configured)
		v.PreviewsUnknown = err != nil
		for _, p := range previews {
			row := ExecutorPreview{ExecutionPreview: p}
			if !p.RunID.IsZero() {
				run, err := h.work.Run(r.Context(), p.RunID)
				if err != nil {
					WriteFault(w, err)
					return
				}
				row.ChangeID = run.ChangeID
			}
			v.Previews = append(v.Previews, row)
		}
	}
	v.VersionMismatch = v.Version != "" && v.Version != v.AppTownVersion
	WriteJSON(w, 200, v)
}
