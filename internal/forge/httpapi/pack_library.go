package httpapi

import (
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/stock"
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
		if err := h.ways.OfferPack(r.Context(), name, in.Enabled, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, PackOffered{OK: true, Name: name, Enabled: in.Enabled})
	})
}

// SavePack: POST /api/v1/packs — create or update a CUSTOM pack's
// declaration (stock packs are read-only; include one in a pack of your
// own). A declaration that cannot be resolved is refused. Validates policy
// rules hard; missing guidance/workflows come back as warnings — packs may
// reference library content that arrives later.
func (h PackLibrary) SavePack(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPacks, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	// Strict: a field a pack does not have (autoAdopt, a misspelling) is
	// refused, not silently dropped.
	jsonCommand(w, r, jsonBody{strict: true}, func(in PackSave) {
		if strings.TrimSpace(in.Name) == "" {
			WriteError(w, 400, "name required")
			return
		}

		if !project.ValidSlug(in.Name) {
			WriteError(w, 400, "pack name must match %s", project.SlugShape)
			return
		}
		if existing, err := h.ways.Pack(r.Context(), in.Name); err == nil && existing.Origin == "stock" {
			WriteError(w, 409, "%q is a stock pack — make a pack of your own that includes it", in.Name)
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
		var warnings []string
		for _, g := range in.Guidance {
			if _, err := h.ways.BlockByName(r.Context(), g); err != nil {
				warnings = append(warnings, "guidance block not in store: "+g)
			}
		}
		for _, wf := range in.Workflows {
			if _, _, err := h.ways.WorkflowVersion(r.Context(), wf, 0); err != nil {
				warnings = append(warnings, "workflow not in library: "+wf)
			}
		}
		for _, pol := range in.Policies {
			if pol.Rule == "" && h.ways.LibraryPolicy(r.Context(), pol.Name) == nil {
				warnings = append(warnings, "policy not in library: "+pol.Name)
			}
		}
		if in.Toolchain != "" {
			if _, err := stock.FS.ReadFile("toolchains/" + in.Toolchain + ".toml"); err != nil {
				warnings = append(warnings, "no stock toolchain: "+in.Toolchain)
			}
		}
		// Everything the author declares is kept, including what the
		// editor does not edit, so an unchanged save publishes what was
		// written. The platform tier is not the editor's.
		spec := encodedJSON(wow.PackSpec{Resources: in.Resources, Includes: in.Includes, Replaces: in.Replaces, Drops: in.Drops, Clears: in.Clears,
			Guidance: in.Guidance, Workflows: in.Workflows, Policies: in.Policies, Toolchain: in.Toolchain,
			Services: in.Services, Extensions: in.Extensions, Requires: in.Requires})
		version, err := h.ways.PublishPackVersion(r.Context(), in.Name, in.Description, spec, "", strings.TrimSpace(in.VersionName))
		if err != nil {
			WriteFault(w, err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "pack.saved", in.Name, map[string]any{
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
	if err := h.ways.DeletePack(r.Context(), name, Actor(r)); err != nil {
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
// table, so an agent credential is refused before any effect. Packs that
// include this one without a pin get a follow version, not recommended.
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
		if err := h.ways.RecommendPack(r.Context(), name, in.Version, Actor(r), in.WhatIf); err != nil {
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
