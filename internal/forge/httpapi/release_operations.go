package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

type ReleaseOperations struct {
	projects   *project.Projects
	access     *identity.Service
	deployment *deployment.Service
	previews   *preview.Service
	work       *workflow.Service
	db         *database.DB
	dataDir    string
	target     string
	now        func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewReleaseOperations(projects *project.Projects, access *identity.Service, deployment *deployment.Service, previews *preview.Service, work *workflow.Service, db *database.DB, dataDir string, target string, now func() time.Time) ReleaseOperations {
	return ReleaseOperations{projects: projects, access: access, deployment: deployment, previews: previews, work: work, db: db, dataDir: dataDir, target: target, now: now}
}

// ReleasePlan is what would ship if the release button were pressed now.
type ReleasePlan struct {
	Configured       bool            `json:"configured"`
	Deployment       string          `json:"deployment"`
	ConfigError      string          `json:"configError,omitempty"`
	URL              string          `json:"url,omitempty"`
	Head             *ReleaseHead    `json:"head,omitempty"`
	Contract         string          `json:"contract"`
	ContractError    string          `json:"contractError,omitempty"`
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
	out := ReleasePlan{Deployment: "lan"}
	cfg, cfgErr := h.deployment.IntranetConfig(r.Context())
	if cfgErr != nil {
		out.ConfigError = cfgErr.Error()
	} else {
		out.Configured = true
		out.Deployment = cfg.ReleaseDeployment
		out.URL = "https://" + p.Slug + "." + cfg.AppsDomain
	}
	repo := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
	if head, err := gitexec.Run(r.Context(), repo, "log", "-1", "--format=%H%x00%s", p.DefaultBranch); err == nil {
		if sha, subject, ok := strings.Cut(head, "\x00"); ok {
			out.Head = &ReleaseHead{SHA: sha, Subject: subject}
		}
	}
	if c, err := docker.ReadWorkspaceContract(repo); err == nil {
		switch {
		case c.PublicPart().Static != "":
			out.Contract = "static"
			if _, derr := os.Stat(filepath.Join(repo, "Dockerfile")); derr != nil {
				out.Contract = "from-source" // same refusal + affordance: land a Dockerfile
			}
		case c.PublicPart().StaticBuild != nil:
			out.Contract = "static"
			if _, derr := docker.SelectedPath(repo, filepath.Join(c.PublicPart().StaticBuild.Context, "Dockerfile")); derr != nil {
				out.Contract = "from-source"
			}
		case c.PublicPart().Image != "":
			out.Contract = "image"
		default:
			out.Contract = "from-source"
			if _, derr := os.Stat(filepath.Join(repo, "Dockerfile")); derr == nil {
				out.Contract = "source-packaged" // AppTown builds its Dockerfile at release
			}
		}
	} else if err == docker.ErrNoContract {
		out.Contract = "none"
	} else {
		out.Contract = "invalid"
		out.ContractError = err.Error()
	}
	relEnv := out.Deployment
	var lastNum int64
	var lastAt string
	last, err := h.deployment.Releases.Last(r.Context(), p.ID, relEnv)
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

// LANRelease: POST /projects/{slug}/release — the button/CLI verb.
// Gate: promotion only promotes what is already LANDED on the default
// branch (landing gates were the quality bar; release is distribution).
// Anyone holding release:promote on the project may press it; the audit
// row records who.
func (h ReleaseOperations) LANRelease(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapReleasePromote, h.resource(r.Context(), p), access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in ReleaseRequest) {
		// A release, once begun, finishes though the request ends. A vanity
		// domain sticks to the project once a release with it is admitted;
		// DNS for it is platform-owned (IaC) — AppTown only labels the
		// stack. A refused release changes nothing.
		d := strings.TrimSpace(in.Domain)
		res, code, err := h.deployment.ReleaseLAN(context.WithoutCancel(r.Context()), p, Actor(r), deployment.LANReleaseRequest{Domain: d, AcceptUncopiedData: in.AcceptUncopiedData})
		if err != nil {
			WriteError(w, code, "%v", err)
			return
		}
		WriteJSON(w, http.StatusAccepted, res)
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

func (h ReleaseOperations) resource(ctx context.Context, p *project.Project) access.Resource {
	res := project.ObservedResource(ctx, h.projects, p)
	res.Type, res.ID = "release-target", h.target
	return res
}

// ReleaseRequest is POST /projects/{slug}/release: an optional hostname to
// serve the release on, kept for later releases.
type ReleaseRequest struct {
	Domain string `json:"domain"`
	// AcceptUncopiedData is a person's choice to release onto an intranet
	// deployment holding data AppTown cannot copy now; the release
	// records it.
	AcceptUncopiedData bool `json:"acceptUncopiedData,omitempty"`
}
