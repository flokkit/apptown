package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

type ReleaseOperations struct {
	projects   *project.Projects
	deployment *deployment.Service
	previews   *preview.Service
	work       *workflow.Service
	db         *database.DB
	dataDir    string
	now        func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewReleaseOperations(projects *project.Projects, deployment *deployment.Service, previews *preview.Service, work *workflow.Service, db *database.DB, dataDir string, now func() time.Time) ReleaseOperations {
	return ReleaseOperations{projects: projects, deployment: deployment, previews: previews, work: work, db: db, dataDir: dataDir, now: now}
}

// ReleasePlan is what would ship if the release button were pressed now.
type ReleasePlan struct {
	DeploymentID    ids.ID                     `json:"deploymentId,omitzero"`
	DeploymentLabel string                     `json:"deploymentLabel,omitempty"`
	DefaultRevision int64                      `json:"defaultRevision"`
	Address         *deployment.AddressReading `json:"address,omitempty"`
	Configured      bool                       `json:"configured"`
	Deployment      string                     `json:"deployment"`
	ConfigError     string                     `json:"configError,omitempty"`
	URL             string                     `json:"url,omitempty"`
	// Published is whether the project's latest bundle is in its container
	// registry by digest: only such a bundle is released to the intranet.
	Published bool `json:"published"`
	// Head is the commit the release would ship: the published bundle's,
	// or main's tip when there is none.
	Head             *ReleaseHead    `json:"head,omitempty"`
	LastRelease      *LastRelease    `json:"lastRelease,omitempty"`
	ChangesSinceLast []ShippedChange `json:"changesSinceLast"`
}

// ReleaseHead is the commit a release would ship.
type ReleaseHead struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// LastRelease is the deployment's most recent release.
type LastRelease struct {
	Number int64  `json:"number"`
	At     string `json:"at"`
}

// ShippedChange is a change landed since the last release.
type ShippedChange struct {
	ID    ids.ID `json:"id"`
	Title string `json:"title"`
}

// ReleasePlan: GET /projects/{slug}/release/plan — what WOULD ship if
// you pressed the button: current head, the changes shipped since the last
// release, the target. The release page shows this before asking.
func (h ReleaseOperations) ReleasePlan(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out := ReleasePlan{}
	destination, err := h.deployment.ReadReleaseDestination(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	selected := destination.Default
	out.DefaultRevision = selected.Revision
	if selected.DeploymentID.IsZero() {
		out.ConfigError = "Choose a release environment in project settings."
	} else {
		out.DeploymentID, out.Deployment, out.DeploymentLabel = selected.DeploymentID, destination.Name, destination.Label
		out.ConfigError = selected.EligibilityIssue
		out.Configured = selected.EligibilityIssue == ""
		out.Address = destination.Address
		if address := out.Address; address != nil && address.Verified {
			host := address.GeneratedHostname
			if address.CustomHostname != "" {
				host = address.CustomHostname
			}
			if host != "" {
				out.URL = "https://" + host
			}
		}
	}
	bundle, err := h.deployment.Bundles.Latest(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	rev := p.DefaultBranch
	if bundle != nil {
		out.Published, rev = bundle.ByDigest(), bundle.ContractRevision
	}
	repo, err := p.Repository(h.dataDir)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if rev != "" {
		if head, err := gitexec.Run(r.Context(), repo, "log", "-1", "--format=%H%x00%s", rev, "--"); err == nil {
			if sha, subject, ok := strings.Cut(head, "\x00"); ok {
				out.Head = &ReleaseHead{SHA: sha, Subject: subject}
			}
		}
	}
	var lastNum int64
	var lastAt string
	last, err := h.deployment.Releases.LastReleasedForDeployment(r.Context(), out.DeploymentID)
	if found, err := database.Lookup(err); err != nil {
		WriteFault(w, err)
		return
	} else if found {
		lastNum, lastAt = last.Number, last.CreatedAt
		out.LastRelease = &LastRelease{Number: lastNum, At: lastAt}
	}
	// What shipped since the last release, through Work's door: a zero
	// cutoff (no release yet) is all shipped history, thirty newest.
	var after time.Time
	if lastAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, lastAt); err == nil {
			after = t
		} else if t, err := time.Parse(time.RFC3339, lastAt); err == nil {
			after = t
		}
	}
	shipped, err := h.work.ShippedChangesSince(r.Context(), p.ID, after, 30)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out.ChangesSinceLast = []ShippedChange{}
	for _, c := range shipped {
		out.ChangesSinceLast = append(out.ChangesSinceLast, ShippedChange{ID: c.ID, Title: c.Title})
	}
	WriteJSON(w, 200, out)
}

// LANRelease retains the existing default-release endpoint. Deployment owns
// selection, current authority and the durable request; execution is a job.
func (h ReleaseOperations) LANRelease(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in ReleaseRequest) {
		var custom *string
		if domain := strings.TrimSpace(in.Domain); domain != "" {
			custom = &domain
		}
		accepted, err := h.deployment.RequestDefaultRelease(r.Context(), deployment.DefaultReleaseSelection{ProjectID: p.ID, ExpectedRevision: in.DefaultRevision, AddressRevision: in.AddressRevision, CustomHostname: custom, AcceptUncopiedData: in.AcceptUncopiedData, AcceptUncheckedCopy: in.AcceptUncheckedCopy})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, deployment.LANReleaseStarted{OK: true, Number: accepted.Number, URL: accepted.URL, State: accepted.State})
	})
}

// ReleasePreview: POST /projects/{slug}/releases/{number}/preview —
// boot the EXACT bytes of a past release and serve them on the preview
// tier. Time travel is cheap when a release is a digest: the same image
// that passed the gates runs again beside today's version. A look at an old
// version, never a restore of one: sidecar services boot fresh (throwaway
// pvrel stacks) and the data volume is blank — old code never touches
// today's data.
func (h ReleaseOperations) ReleasePreview(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	n, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || n <= 0 {
		WriteError(w, 400, "bad release number")
		return
	}
	out, err := h.previews.RequestReleasePreview(r.Context(), preview.ReleaseSelection{ProjectID: p.ID, Number: n})
	if err != nil {
		writeReleasePreviewError(w, err)
		return
	}
	if !out.Reused {
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "release.preview", fmt.Sprintf("release %d", n), map[string]any{"ref": out.Image, "source": out.Revision})
	}
	WriteJSON(w, 200, ReleasePreviewStarted{URL: out.URL})
}

// ReleasePreviewStarted is where a past release is now served.
type ReleasePreviewStarted struct {
	URL string `json:"url"`
}

func writeReleasePreviewError(w http.ResponseWriter, err error) {
	var execution *preview.ExecutionError
	switch {
	case WriteRefusal(w, err):
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		WriteError(w, http.StatusUnprocessableEntity, "%v", err)
	case errors.As(err, &execution):
		WriteError(w, http.StatusBadGateway, "%v", err)
	default:
		WriteFault(w, err)
	}
}

// ReleaseRequest is POST /projects/{slug}/release: an optional hostname to
// serve the release on, kept for later releases.
type ReleaseRequest struct {
	DefaultRevision     *int64 `json:"defaultRevision,omitempty"`
	AcceptUncheckedCopy bool   `json:"acceptUncheckedCopy,omitempty"`
	AddressRevision     *int64 `json:"addressRevision,omitempty"`
	Domain              string `json:"domain"`
	// AcceptUncopiedData is a person's choice to release onto an intranet
	// deployment holding data AppTown cannot copy now; the release
	// records it.
	AcceptUncopiedData bool `json:"acceptUncopiedData,omitempty"`
}
