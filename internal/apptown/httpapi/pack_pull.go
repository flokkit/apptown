package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type PackPull struct {
	adoption *adoption.Service
	projects *project.Projects
	access   *identity.Service
	ways     *wow.Service
	db       *database.DB
	now      func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewPackPull(adoption *adoption.Service, projects *project.Projects, access *identity.Service, ways *wow.Service, db *database.DB, now func() time.Time) PackPull {
	return PackPull{adoption: adoption, projects: projects, access: access, ways: ways, db: db, now: now}
}

// PackPullPlan: GET /projects/{slug}/pack/pull-plan[?pack=name]
func (h PackPull) PackPullPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	pack, err := h.resolveProjectPack(r.Context(), p, r.URL.Query().Get("pack"))
	if err != nil {
		WriteError(w, 422, "%v", err)
		return
	}
	plan, err := h.adoption.PackPullPlan(r.Context(), p.ID, *pack)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, plan)
}

// PackPull: POST /projects/{slug}/pack/pull {pack?} — a person's
// explicit act: selects the pack's added lanes, deselects the removed,
// records the pack, and writes the adoption revision that results. It no
// longer adopts guidance (design/subsystems/ways-of-working-versions.md §1.2, §9 step
// 1): blocks the pack now carries are listed for a person to add under
// Guidance, and a contradicting one is refused there too. Amend-gated.
func (h PackPull) PackPull(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	authorityResource, err := h.projects.AuthorityResource(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectWayOfWorking, authorityResource, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct{ Pack string }) {
		pack, err := h.resolveProjectPack(r.Context(), p, in.Pack)
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		pulled, err := h.adoption.PullPack(r.Context(), p.ID, *pack, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		plan := pulled.Plan
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "pack.pulled", pack.Name, map[string]any{
			"installed": pulled.Installed, "removed": pulled.Removed, "guidanceNotAdopted": plan.Guidance, "conflicting": plan.Conflicting,
			"keptReferenced": plan.KeptReferenced, "deprecatedSkipped": plan.Deprecated})
		// Guidance is not adopted here: what the pack carries that the project
		// does not is reported, and a person adds it under Guidance.
		WriteJSON(w, 200, PackPulled{Pack: pack.Name, Installed: pulled.Installed,
			Removed: pulled.Removed, GuidanceToAdd: plan.Guidance, Conflicting: plan.Conflicting, KeptReferenced: plan.KeptReferenced, Deprecated: plan.Deprecated})
	})
}

// resolveProjectPack: the pack named in the request. A project has several
// packs, so a pull always names the one it moves.
func (h PackPull) resolveProjectPack(ctx context.Context, p *project.Project, requested string) (*wow.Pack, error) {
	name := requested
	if name == "" {
		return nil, fmt.Errorf("name the pack to pull: a project can be on several")
	}
	pack, err := packNamed(ctx, h.ways, name)
	if err != nil {
		return nil, err
	}
	if pack == nil {
		return nil, fmt.Errorf("no such pack %q", name)
	}
	return pack, nil
}

// PackPulled is what pulling a pack changed and what it left for a person.
type PackPulled struct {
	Pack           string   `json:"pack"`
	Installed      []string `json:"installed"`
	Removed        []string `json:"removed"`
	GuidanceToAdd  []string `json:"guidanceToAdd"`
	Conflicting    []string `json:"conflicting"`
	KeptReferenced []string `json:"keptReferenced"`
	Deprecated     []string `json:"deprecated"`
}
