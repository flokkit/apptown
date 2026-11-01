package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type PackLibrary struct {
	ways    *wow.Service
	whatIfs *conformance.Service
	access  *identity.Service
	db      *database.DB
	now     func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewPackLibrary(ways *wow.Service, whatIfs *conformance.Service, access *identity.Service, db *database.DB, now func() time.Time) PackLibrary {
	return PackLibrary{ways: ways, whatIfs: whatIfs, access: access, db: db, now: now}
}

// SetPackEnabled: POST /packs/{name}/enabled {enabled} — the
// instance's switch. The last enabled pack cannot be turned off: a project
// must be makeable from something.
func (h PackLibrary) SetPackEnabled(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPacks, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	name := r.PathValue("name")
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Enabled bool `json:"enabled"`
	}) {
		if err := h.ways.OfferPack(r.Context(), name, in.Enabled, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, PackOffered{OK: true, Name: name, Enabled: in.Enabled})
	})
}

// SavePack: POST /api/v1/packs — create or update a CUSTOM pack's
// declaration (stock packs are read-only; copy one into a pack of your
// own). A declaration that names a block,
// workflow or policy the library does not have, is refused; an unknown
// toolchain is only a warning (a starter file, not pinned).
func (h PackLibrary) SavePack(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPacks, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	// Strict: a field a pack does not have (autoAdopt, a misspelling) is
	// refused, not silently dropped.
	jsonCommand(w, r, jsonBody{strict: true}, func(in PackSave) {
		if err := schema.CheckAPIVersion(in.APIVersion); err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			WriteError(w, 400, "name required")
			return
		}

		if !project.ValidSlug(in.Name) {
			WriteError(w, 400, "pack name must match %s", project.SlugShape)
			return
		}
		if existing, err := h.ways.Pack(r.Context(), in.Name); err == nil && existing.Origin == "stock" {
			WriteError(w, 409, "%q is a stock pack — make a pack of your own beside it, and a project takes both", in.Name)
			return
		}
		for _, pol := range in.Policies {
			if pol.Name == "" {
				WriteError(w, 400, "every pack policy needs a name")
				return
			}
			// A pack names library policies; its versions pin their
			// definitions. A definition written here would bypass the library.
			if pol.Rule != "" || len(pol.With) > 0 {
				WriteError(w, 400, "policy %q: a pack names a library policy; add the definition to the policy library and name it here", pol.Name)
				return
			}
		}
		// A block, workflow or policy the library lacks is refused by the
		// publication (wow.ErrUnpinned). A toolchain is a starter file,
		// not pinned, so a missing one is only a warning.
		var warnings []string
		if in.Toolchain != "" {
			if _, err := h.ways.Toolchain(in.Toolchain); err != nil {
				warnings = append(warnings, "no stock toolchain: "+in.Toolchain)
			}
		}
		// Everything the author declares is kept, including what the
		// editor does not edit, so an unchanged save publishes what was
		// written. The platform tier is not the editor's.
		spec := encodedJSON(wow.PackSpec{
			Guidance: in.Guidance, Workflows: in.Workflows, Policies: in.Policies, Toolchain: in.Toolchain, Tools: in.Tools,
			Services: in.Services, Extensions: in.Extensions, Requires: in.Requires})
		version, err := h.ways.PublishPackVersion(r.Context(), in.Name, in.Description, spec, "", strings.TrimSpace(in.VersionName))
		if err != nil {
			WriteFault(w, err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, actor(r), ids.Nil, "pack.saved", in.Name, map[string]any{
			"guidance": len(in.Guidance), "workflows": in.Workflows, "policies": len(in.Policies), "warnings": warnings})
		WriteJSON(w, 200, PackSaved{OK: true, Version: int64(version), Warnings: warnings})
	})
}

// DeletePack: DELETE /api/v1/packs/{name} — custom packs only.
func (h PackLibrary) DeletePack(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPacks, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	name := r.PathValue("name")
	if err := h.ways.DeletePack(r.Context(), name, actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// PackOffered is a pack's offered state after a change.
type PackOffered struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// PackSave is a pack's declaration as the editor sends it, with an
// optional name for the version saving it makes.
type PackSave struct {
	wow.Pack
	VersionName string `json:"versionName,omitempty"`
}

// PackSaved reports the version the save made (or kept, when nothing
// changed) and what the saved pack references that is not there yet.
type PackSaved struct {
	OK       bool     `json:"ok"`
	Version  int64    `json:"version"`
	Warnings []string `json:"warnings"`
}

// RecommendPackVersion: POST /api/v1/packs/{name}/recommended {version,
// whatIf?} — with whatIf, the what-if the person decided on, which must be
// of this pack and version and done; its receipt names it. Otherwise
// the version new projects get and projects are offered as the update. A
// person's act: standards:manage-packs is in no agent scope or instrument
// table, so an agent credential is refused before any effect.
func (h PackLibrary) RecommendPackVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPacks, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	name := r.PathValue("name")
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Version int64  `json:"version"`
		WhatIf  ids.ID `json:"whatIf"`
	}) {
		if !in.WhatIf.IsZero() {
			wi, err := h.whatIfs.WhatIf(r.Context(), in.WhatIf)
			if err != nil {
				WriteFault(w, err)
				return
			}
			if wi.Pack != name || wi.Version != in.Version || wi.State != conformance.WhatIfDone {
				WriteError(w, 409, "what-if #%d is of %s v%d and %s; recommend %s v%d on one of that version that finished", wi.ID, wi.Pack, wi.Version, wi.State, name, in.Version)
				return
			}
		}
		if err := h.ways.RecommendPack(r.Context(), name, in.Version, actor(r), in.WhatIf); err != nil {
			WriteFault(w, err)
			return
		}
		history, err := h.ways.PackHistory(r.Context(), name)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, history)
	})
}
