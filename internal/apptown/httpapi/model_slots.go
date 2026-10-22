package httpapi

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type ModelSlots struct {
	models   *model.Slots
	projects *project.Projects
	ways     *wow.Service
	access   *identity.Service
	db       *database.DB
	now      func() time.Time // AppTown's clock; nil is the real one
}

func NewModelSlots(models *model.Slots, projects *project.Projects, ways *wow.Service, access *identity.Service, db *database.DB, now func() time.Time) ModelSlots {
	return ModelSlots{models: models, projects: projects, ways: ways, access: access, db: db, now: now}
}

// ModelSlots: GET /api/v1/model-slots — the full matrix: slots (with
// gloss, fallback, consumers, recent spend), profile columns, per-project
// profile assignments, and explicit project overrides.
func (h ModelSlots) ModelSlots(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.models.Profiles(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	cells, err := h.models.Cells(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	slotSet := map[string]bool{}
	for _, s := range model.StockSlots {
		slotSet[s] = true
	}
	for s := range cells {
		slotSet[s] = true
	}
	fallbacks, err := h.models.Fallbacks(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	consumers, err := h.slotConsumers(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	// Tokens burned per slot over the last 7 days, from step receipts.
	// Honest scope: workflow steps only; chat turns aren't counted yet.
	spend, err := workflow.SlotSpendIn(r.Context(), h.db, clock.Or(h.now)().Add(-7*24*time.Hour))
	if err != nil {
		WriteFault(w, err)
		return
	}
	var slots []ModelSlotView
	for _, s := range model.StockSlots {
		delete(slotSet, s)
		slots = append(slots, h.slotView(s, cells, fallbacks, consumers, spend))
	}
	var extra []string
	for s := range slotSet {
		extra = append(extra, s)
	}
	sort.Strings(extra)
	for _, s := range extra {
		slots = append(slots, h.slotView(s, cells, fallbacks, consumers, spend))
	}
	var projects []ModelSlotProject
	all, err := h.projects.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	for _, p := range all {
		projects = append(projects, ModelSlotProject{Slug: p.Slug, Name: p.Name, Profile: p.ModelProfile})
	}
	stored, err := h.models.Overrides(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	var overrides []ModelSlotOverride
	for _, o := range stored {
		overrides = append(overrides, ModelSlotOverride{Project: o.ProjectSlug, Slot: o.Slot, Pattern: o.Pattern})
	}
	WriteJSON(w, 200, ModelSlotMatrix{
		Profiles: profiles, Slots: slots, Projects: projects, Overrides: overrides,
		SpendNote: "spend counts workflow steps over the last 7 days; chat turns are not counted yet",
	})
}

// PutModelSlot: PUT /api/v1/model-slots {slot, pattern, project, profile}
// Instance cells carry a profile (default 'standard'); a project override
// carries none (it beats every profile). Empty pattern deletes the cell.
func (h ModelSlots) PutModelSlot(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapModelsSlots, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "slot (no slash) required"}, func(in struct {
		Slot    string `json:"slot"`
		Pattern string `json:"pattern"`
		Project string `json:"project"`
		Profile string `json:"profile"`
	}) {
		if in.Slot == "" || strings.Contains(in.Slot, "/") {
			WriteError(w, 400, "slot (no slash) required")
			return
		}

		var pid ids.ID
		profile := ""
		if in.Project != "" {
			p, err := h.projects.BySlug(r.Context(), in.Project)
			if err != nil {
				writeReadFault(w, err, "no such project %q", in.Project)
				return
			}
			pid = p.ID
		} else {
			profile = in.Profile
			if profile == "" {
				profile = model.StandardProfile
			}
			if !model.ValidProfile(profile) {
				WriteError(w, 400, "profile must be a short lowercase word (letters, digits, dashes)")
				return
			}
		}
		if err := h.models.Set(r.Context(), in.Slot, pid, in.Project, profile, in.Pattern, Actor(r)); err != nil {
			WriteError(w, 500, "saving the model slot: %v", err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// PutProjectModelProfile: PUT /api/v1/projects/{slug}/model-profile
// {profile} — one visible act assigns a project its demand level.
func (h ModelSlots) PutProjectModelProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectModelProfile, project.ObservedResource(r.Context(), h.projects, p), access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "profile must be a short lowercase word (letters, digits, dashes)"}, func(in struct {
		Profile string `json:"profile"`
	}) {
		if !model.ValidProfile(in.Profile) {
			WriteError(w, 400, "profile must be a short lowercase word (letters, digits, dashes)")
			return
		}

		if err := h.projects.SetModelProfile(r.Context(), p.ID, in.Profile); err != nil {
			WriteError(w, 500, "saving the model profile: %v", err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "model-profile.set", in.Profile, map[string]any{"project": p.Slug})
		WriteJSON(w, 200, Done{OK: true})
	})
}

// slotConsumers: who demands each slot — built-in surfaces plus workflows
// whose YAML names it. Changing a cell is safer when its blast radius is
// visible first.
func (h ModelSlots) slotConsumers(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{
		"default": {"any step without a model named", "end of every fallback chain"},
		"chat":    {"project chat"},
		"worker":  {"delegate: carries out briefs (chat + steps)"},
		"scout":   {"delegate: quick lookups (chat + steps)"},
		"draft":   {"workflow drafting assistant"},
		"fast":    {"change summaries", "briefs", "guidance checks", "setup probes"},
	}
	// Consumers are library workflows now (design/decisions/workflows-as-lanes.md);
	// the sample names the workflow, not a project copy.
	type wf struct{ name, yaml, slug string }
	entries, err := h.ways.Workflows(ctx)
	if err != nil {
		return nil, err
	}
	var wfs []wf
	for _, e := range entries {
		wfs = append(wfs, wf{e.Name, e.YAML, "library"})
	}
	for _, slot := range model.StockSlots {
		needle := "model: " + slot
		n, sample := 0, ""
		for _, x := range wfs {
			if strings.Contains(x.yaml, needle) {
				n++
				if sample == "" {
					sample = x.slug + "/" + x.name
				}
			}
		}
		if n == 1 {
			out[slot] = append(out[slot], "workflow "+sample)
		} else if n > 1 {
			out[slot] = append(out[slot], fmt.Sprintf("%d workflows (e.g. %s)", n, sample))
		}
	}
	return out, nil
}

func (h ModelSlots) slotView(s string, cells map[string]map[string]string, fallbacks map[string]string,
	consumers map[string][]string, spend map[string]map[string]int64) ModelSlotView {
	v := ModelSlotView{Slot: s, Gloss: model.Gloss[s], Patterns: map[string]string{}}
	if c := cells[s]; c != nil {
		v.Patterns = c
	}
	if f := fallbacks[s]; f != "" {
		v.Fallback = f
	} else if s != "default" {
		v.Fallback = "default"
	}
	if c := consumers[s]; len(c) > 0 {
		v.Consumers = c
	}
	v.Spend7d = spend[s]
	return v
}

// ModelSlotMatrix is the whole model-slot picture: the profile columns, each
// slot, which profile each project runs at, and explicit project overrides.
type ModelSlotMatrix struct {
	Profiles  []string            `json:"profiles"`
	Slots     []ModelSlotView     `json:"slots"`
	Projects  []ModelSlotProject  `json:"projects"`
	Overrides []ModelSlotOverride `json:"overrides"`
	SpendNote string              `json:"spendNote"`
}

// ModelSlotView is one slot: its patterns per profile, what it falls back to,
// who asks for it, and what it spent in the last week.
type ModelSlotView struct {
	Slot      string            `json:"slot"`
	Gloss     string            `json:"gloss"`
	Patterns  map[string]string `json:"patterns"`
	Fallback  string            `json:"fallback,omitempty"`
	Consumers []string          `json:"consumers,omitempty"`
	Spend7d   map[string]int64  `json:"spend7d,omitzero"`
}

// ModelSlotProject is the profile one project runs at.
type ModelSlotProject struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Profile string `json:"profile"`
}

// ModelSlotOverride is one project's explicit model for a slot.
type ModelSlotOverride struct {
	Project string `json:"project"`
	Slot    string `json:"slot"`
	Pattern string `json:"pattern"`
}
