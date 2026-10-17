package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/wow"
)

type GuidanceBlocks struct {
	ways     *wow.Service
	adoption *adoption.Service
	access   *identity.Service
}

func NewGuidanceBlocks(ways *wow.Service, adoption *adoption.Service, access *identity.Service) GuidanceBlocks {
	return GuidanceBlocks{ways: ways, adoption: adoption, access: access}
}
func (h GuidanceBlocks) ListBlocks(w http.ResponseWriter, r *http.Request) {
	out, err := h.ways.Blocks(r.Context(), wow.BlockFilter{Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag")})
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

func (h GuidanceBlocks) CreateBlock(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsGuidance, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Name   string `json:"name"`
		BodyMD string `json:"bodyMd"`
		Tags   string `json:"tags"`
	}) {
		b, err := h.ways.CreateBlock(r.Context(), in.Name, in.BodyMD, in.Tags, Actor(r))
		if err != nil {
			// Unclassified failures keep the 409 this route has always answered.
			WriteFaultOr(w, err, http.StatusConflict)
			return
		}
		WriteJSON(w, 201, b)
	})
}

func (h GuidanceBlocks) GetBlock(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	b, err := h.ways.BlockByID(r.Context(), id)
	if err != nil {
		WriteError(w, 404, "no such block")
		return
	}
	version, ok := QueryInt(w, r, "version", b.LatestVersion)
	if !ok {
		return
	}
	body, err := h.ways.BlockVersionBody(r.Context(), id, version)
	if err != nil {
		WriteError(w, 404, "no such version %d", version)
		return
	}
	b.BodyMD = body
	b.Bytes = len(body)
	versions, err := h.ways.BlockVersions(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	uses, err := h.adoption.BlockUses(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	checks, _ := h.blockVersionChecks(r.Context(), id, version)
	WriteJSON(w, 200, GuidanceBlockDetail{Block: *b, ShownVersion: version, Versions: versions, UsedBy: uses, Checks: checks})
}

// PatchBlock updates tags in place; a body or checks change appends a
// new version (prose and teeth are one versioned artifact).
func (h GuidanceBlocks) PatchBlock(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsGuidance, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	b, err := h.ways.BlockByID(r.Context(), id)
	if err != nil {
		WriteError(w, 404, "no such block")
		return
	}
	// A stock block is forge-shipped and auto-upgraded from stock/guidance/**;
	// editing it in place would be clobbered on the next release. To diverge,
	// make your own pack that excludes the stock block and includes your own.
	if b.Origin == "stock" {
		WriteError(w, 409, "%q is a forge-shipped (stock) guidance block — it is read-only and auto-upgrades on release. To customize, author your own block and swap it in via a pack.", b.Name)
		return
	}
	// Strict: a misspelt key in a check (severty for severity) is refused
	// here, before decoding into the declaration type would drop it.
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		BodyMD *string                         `json:"bodyMd"`
		Tags   *string                         `json:"tags"`
		Checks *[]conformance.CheckDeclaration `json:"checks"`
	}) {
		if in.Checks != nil {
			if err := check.Validate(*in.Checks); err != nil {
				WriteError(w, 400, "%v", err)
				return
			}
		}
		out, err := h.ways.EditBlock(r.Context(), id, wow.BlockPatch{Tags: in.Tags, BodyMD: in.BodyMD, Checks: in.Checks}, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}

		WriteJSON(w, 200, out)
	})
}

func (h GuidanceBlocks) blockVersionChecks(ctx context.Context, blockID ids.ID, version int) ([]conformance.CheckDeclaration, error) {
	raw, err := h.ways.BlockVersionChecks(ctx, blockID, version)
	if err != nil {
		return nil, err
	}
	var checks []conformance.CheckDeclaration
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		return nil, err
	}
	if len(checks) == 0 {
		return nil, nil // the column's absent value is []; in Go, none is nil
	}
	return checks, nil
}

// GuidanceBlockDetail is one guidance block at a shown version, with its
// history, the projects using it and the checks it declares.
type GuidanceBlockDetail struct {
	Block        wow.Block                      `json:"block"`
	ShownVersion int                            `json:"shownVersion"`
	Versions     []wow.BlockVersion             `json:"versions"`
	UsedBy       []adoption.BlockUse            `json:"usedBy"`
	Checks       []conformance.CheckDeclaration `json:"checks"`
}
