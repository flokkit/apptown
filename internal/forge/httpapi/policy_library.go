package httpapi

import (
	"net/http"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/wow"
)

type PolicyLibrary struct {
	ways     *wow.Service
	adoption *adoption.Service
	db       *database.DB
}

func NewPolicyLibrary(ways *wow.Service, adoption *adoption.Service, db *database.DB) PolicyLibrary {
	return PolicyLibrary{ways: ways, adoption: adoption, db: db}
}

// PolicyLibrary: GET /api/v1/policy-library — definitions plus usage
// (which projects adopted each, conformant or not; which packs reference it).
func (h PolicyLibrary) PolicyLibrary(w http.ResponseWriter, r *http.Request) {
	lib, err := h.ways.LibraryPolicies(r.Context())
	if err != nil {
		WriteError(w, 500, "%s", err.Error())
		return
	}
	// Usage as COUNTS (adoption can be wide; detail is lazy — see
	// PolicyUsage). Conformance is only computed for projects that
	// adopted something, once per project.
	adoptCount := map[string]int{}
	warnCount := map[string]int{}
	// A failed read fails the list: counts of zero would say "unused".
	adoptions, err := h.adoption.PolicyAdoptions(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	{
		confCache := map[ids.ID][]adoption.Violation{}
		for _, ad := range adoptions {
			vs, ok := confCache[ad.ProjectID]
			if !ok {
				vs, err = h.adoption.ProjectConformance(r.Context(), h.db, ad.ProjectID)
				if err != nil {
					WriteFault(w, err)
					return
				}
				confCache[ad.ProjectID] = vs
			}
			adoptCount[ad.Policy]++
			for _, v := range vs {
				if v.Policy == ad.Policy {
					warnCount[ad.Policy]++
					break
				}
			}
		}
	}
	// referenced by packs
	inPacks := map[string][]string{}
	packs, err := h.ways.Packs(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, pk := range packs {
		for _, pol := range pk.Policies {
			inPacks[pol.Name] = append(inPacks[pol.Name], pk.Name)
		}
	}
	out := []PolicyLibraryEntry{}
	for _, lp := range lib {
		e := PolicyLibraryEntry{LibraryPolicy: lp, AdoptedBy: adoptCount[lp.Name],
			NonConformant: warnCount[lp.Name], Packs: inPacks[lp.Name],
			Reading: wow.Reading(lp.Rule, lp.With), Fix: wow.Fix(lp.Rule, lp.With)}
		if e.Packs == nil {
			e.Packs = []string{}
		}
		out = append(out, e)
	}
	WriteJSON(w, 200, PolicyLibraryList{Library: out})
}

// PolicyUsage: GET /api/v1/policy-library/{name}/usage — the full
// adopted-by list, fetched on expand.
func (h PolicyLibrary) PolicyUsage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	adoptions, err := h.adoption.PolicyAdoptions(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	type proj struct {
		id         ids.ID
		slug, name string
	}
	var projs []proj
	for _, ad := range adoptions {
		if ad.Policy == name {
			projs = append(projs, proj{ad.ProjectID, ad.ProjectSlug, ad.ProjectName})
		}
	}
	out := []PolicyUsage{}
	for _, p := range projs {
		u := PolicyUsage{Project: p.name, Slug: p.slug, Conformant: true, Violations: []adoption.Violation{}}
		violations, err := h.adoption.ProjectConformance(r.Context(), h.db, p.id)
		if err != nil {
			WriteFault(w, err)
			return
		}
		for _, v := range violations {
			if v.Policy == name {
				u.Conformant = false
				u.Violations = append(u.Violations, v)
			}
		}
		out = append(out, u)
	}
	WriteJSON(w, 200, PolicyUsageList{UsedBy: out})
}

// SaveLibraryPolicy: POST /api/v1/policy-library — create/update a
// CUSTOM definition (stock is clone-only).
func (h PolicyLibrary) SaveLibraryPolicy(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "name required"}, func(in wow.LibraryPolicy) {
		if strings.TrimSpace(in.Name) == "" {
			WriteError(w, 400, "name required")
			return
		}

		if err := h.ways.SaveLibraryPolicy(r.Context(), in, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// DeleteLibraryPolicy: DELETE /api/v1/policy-library/{name} — custom
// only; projects that adopted it keep their copies (adoption pins).
func (h PolicyLibrary) DeleteLibraryPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.ways.DeleteLibraryPolicy(r.Context(), name, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// PolicyLibraryEntry is a library policy with how widely it is adopted and
// kept, in plain words.
type PolicyLibraryEntry struct {
	wow.LibraryPolicy
	AdoptedBy     int      `json:"adoptedBy"`
	NonConformant int      `json:"nonConformant"`
	Packs         []string `json:"packs"`
	Reading       string   `json:"reading"`
	Fix           string   `json:"fix"`
}

// PolicyUsage is one project that adopted a policy and whether it conforms.
type PolicyUsage struct {
	Project    string               `json:"project"`
	Slug       string               `json:"slug"`
	Conformant bool                 `json:"conformant"`
	Violations []adoption.Violation `json:"violations"`
}

// PolicyLibraryList is the instance's policy library.
type PolicyLibraryList struct {
	Library []PolicyLibraryEntry `json:"library"`
}

// PolicyUsageList is every project that adopted a policy.
type PolicyUsageList struct {
	UsedBy []PolicyUsage `json:"usedBy"`
}
