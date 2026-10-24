package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type Packs struct {
	ways      *wow.Service
	db        *database.DB
	inventory SteeringInventory
}

func NewPacks(ways *wow.Service, db *database.DB, inventory SteeringInventory) Packs {
	return Packs{ways: ways, db: db, inventory: inventory}
}

// Packs: GET /api/v1/packs — packs plus their capability requirements
// resolved against the instance's bindings (the wizard's "✓ scanner → trivy"
// line, policy.md §2 layer 2).
// PackView is a pack as the library lists it, with what it requires of the
// instance and what it adds to every agent's context.
type PackView struct {
	wow.Pack
	Requires []packRequirement `json:"requires"`
	// GuidanceBytes: what the pack's blocks add to every agent's context
	// at their latest versions, so bloat shows on the card.
	// GeneratedBytes: the instance's generated sections every step gets
	// on top, whichever pack (a project's own integrations come later).
	GuidanceBytes  int `json:"guidanceBytes"`
	GeneratedBytes int `json:"generatedBytes"`
	// Declaration is what the pack's author wrote: its inclusion and its own
	// items. The embedded pack is the resolved contents of the version it
	// offers, which is what applying it installs.
	Declaration wow.PackSpec `json:"declaration"`
	// StaticStarter: the pack's starter app is static files with no server
	// (wow.Service.StarterIsStatic), so the wizard offers no services.
	StaticStarter bool `json:"staticStarter,omitempty"`
}

func (h Packs) Packs(w http.ResponseWriter, r *http.Request) {
	packs, err := h.ways.Packs(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	sections, err := h.inventory.sections(r.Context(), ids.Nil)
	if err != nil {
		WriteFault(w, err)
		return
	}
	generated, _ := steeringOverhead(sections)
	out := []PackView{}
	for _, p := range packs {
		if p.Platform {
			continue // not a choice: every repo-backed project is on it
		}
		total := 0
		for _, n := range h.ways.BlockBytesByName(r.Context(), p.Guidance) {
			total += n
		}
		declared, err := h.ways.PackDeclaration(r.Context(), p.Name)
		if err != nil {
			WriteFault(w, err)
			return
		}
		out = append(out, PackView{Pack: p, Declaration: declared.Spec(), Requires: h.packRequirements(r.Context(), p), GuidanceBytes: total, GeneratedBytes: generated,
			StaticStarter: h.ways.StarterIsStatic(p.StarterPack())})
	}
	WriteJSON(w, 200, out)
}

// packRequirements derives the capabilities a pack needs from the step types
// its workflows actually use (derived beats declared — no drift), and
// resolves each against the instance's bindings (integrations.go).
type packRequirement struct {
	Capability string `json:"capability"`
	Bound      bool   `json:"bound"`
	Binding    string `json:"binding,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

var usesRe = regexp.MustCompile(`\buses:\s*([a-z/-]+)`)

func (h Packs) packRequirements(ctx context.Context, pack wow.Pack) []packRequirement {
	caps := map[string]bool{}
	for _, wfName := range pack.Workflows {
		_, yamlSrc, err := h.ways.WorkflowVersion(ctx, wfName, 0)
		if err != nil {
			continue
		}
		for _, m := range usesRe.FindAllStringSubmatch(yamlSrc, -1) {
			switch {
			case m[1] == "agent/pi":
				caps["model"], caps["executor"] = true, true
			case m[1] == "script":
				caps["executor"] = true
			case strings.HasPrefix(m[1], "scm/"):
				caps["scm"] = true
			case m[1] == "deploy" || m[1] == "preview":
				caps["deploy"] = true
			case m[1] == "scan":
				caps["scanner"] = true
			}
		}
	}
	// Stable order matching the run-pin order.
	var out []packRequirement
	for _, c := range []string{"scm", "executor", "model", "deploy", "scanner"} {
		if !caps[c] {
			continue
		}
		req := packRequirement{Capability: c}
		if b, err := integration.Selected(ctx, h.db, c, ""); err == nil {
			req.Bound, req.Binding, req.Provider = true, b.Name, b.Provider
		}
		out = append(out, req)
	}
	return out
}

// PackHistory: GET /api/v1/packs/{name} — a pack's declaration, every
// version with its name and lineage, the recommended version, and why its
// last follow was refused when nothing has been published since.
func (h Packs) PackHistory(w http.ResponseWriter, r *http.Request) {
	history, err := h.ways.PackHistory(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, history)
}
