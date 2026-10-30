package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/stamp"
)

type Fleet struct {
	adoption *adoption.Service
	ways     *wow.Service
}

func NewFleet(adoption *adoption.Service, ways *wow.Service) Fleet {
	return Fleet{adoption: adoption, ways: ways}
}

// Fleet: GET /wow — projects by selected pack version, for the
// projects the caller may see; the count of updates available.
func (h Fleet) Fleet(w http.ResponseWriter, r *http.Request) {
	out := []FleetPack{}
	all, err := h.ways.Packs(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	projects, err := h.adoption.Fleet(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	// Every pack, offered or not: turning a pack off stops new projects
	// taking it, and the projects already on it are still on it.
	for _, p := range all {
		row := FleetPack{Pack: p.Name, Platform: p.Platform, Enabled: p.Enabled, Recommended: p.Version, Versions: []FleetPackVersion{}}
		publications, err := h.ways.PackPublications(r.Context(), p.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		for _, pub := range publications {
			slugs := projects.On[adoption.PackVersion{PackID: p.ID, Version: pub.Version}]
			if slugs == nil {
				slugs = []string{}
			}
			row.Versions = append(row.Versions, FleetPackVersion{Version: pub.Version, Note: pub.Note, At: pub.At, Projects: slugs})
		}
		out = append(out, row)
	}
	WriteJSON(w, 200, FleetView{Packs: out})
}

// FleetView is every pack and which visible projects are on each of its
// versions.
type FleetView struct {
	Packs []FleetPack `json:"packs"`
}

// FleetPack is one pack's versions and the projects on each.
type FleetPack struct {
	Pack     string `json:"pack"`
	Platform bool   `json:"platform,omitempty"`
	// Enabled is whether new projects are offered the pack.
	Enabled bool `json:"enabled"`
	// Recommended is the version new projects get and others are offered.
	Recommended int64              `json:"recommended"`
	Versions    []FleetPackVersion `json:"versions"`
}

// FleetPackVersion is one published version and the projects on it.
type FleetPackVersion struct {
	Version  int64      `json:"version"`
	Projects []string   `json:"projects"`
	Note     string     `json:"note,omitempty"`
	At       stamp.Time `json:"at"`
}
