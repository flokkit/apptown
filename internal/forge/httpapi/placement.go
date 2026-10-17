package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/stamp"
)

// Pack membership is written explicitly at adoption. Views compare the
// immutable receipt with those selected pack versions; startup infers nothing.

type projectPack struct {
	Pack        string     `json:"pack"`
	Version     int64      `json:"version"`
	Recommended int64      `json:"recommended"`
	Platform    bool       `json:"platform,omitempty"`
	Reason      string     `json:"reason"`
	AdoptedBy   string     `json:"adoptedBy"`
	AdoptedAt   stamp.Time `json:"adoptedAt"`
	spec        wow.PackSpec
}

// placementCondition is something a person has to look at.
type placementCondition struct {
	Kind     string   `json:"kind"` // conflict | unplaced | no-platform
	Blocks   []string `json:"blocks,omitempty"`
	Sentence string   `json:"sentence"`
}

type PlacementView struct {
	Packs      []projectPack           `json:"packs"`
	Additions  []adoption.ReceiptBlock `json:"additions"`  // enabled blocks no pack of the project's carries
	Exclusions []adoption.ReceiptBlock `json:"exclusions"` // pack blocks the project disabled
	Conditions []placementCondition    `json:"conditions"`
}

func packGuidance(packs []projectPack) map[string]bool {
	carried := map[string]bool{}
	for _, pp := range packs {
		for _, name := range pp.spec.Guidance {
			carried[name] = true
		}
	}
	return carried
}

// placementOf derives the project's additions, exclusions and conditions
// from its packs and its receipt.
func (h ProjectAdoption) placementOf(ctx context.Context, p *project.Project, ad *adoption.RecordedRevision) (PlacementView, error) {
	packs, err := readProjectPacks(ctx, h.db, p.ID)
	if err != nil {
		return PlacementView{}, err
	}
	view := PlacementView{Packs: packs, Additions: []adoption.ReceiptBlock{}, Exclusions: []adoption.ReceiptBlock{}, Conditions: []placementCondition{}}
	if ad == nil {
		return view, nil
	}
	carried := packGuidance(view.Packs)
	for _, b := range ad.Receipt.Guidance {
		switch {
		case b.Disabled && carried[b.Name]:
			view.Exclusions = append(view.Exclusions, b)
		case !b.Disabled && !carried[b.Name]:
			view.Additions = append(view.Additions, b)
		}
	}
	// Declared conflicts among the blocks in force (§1.4).
	enabled := []adoption.ReceiptBlock{}
	for _, b := range ad.Receipt.Guidance {
		if !b.Disabled {
			enabled = append(enabled, b)
		}
	}
	seen := map[string]bool{}
	for _, b := range enabled {
		var conflicts string
		blk, err := h.ways.BlockByID(ctx, b.BlockID)
		found, err := database.Lookup(err)
		if err != nil {
			return PlacementView{}, err
		}
		if found {
			conflicts = blk.Conflicts
		}
		for _, c := range strings.Split(conflicts, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			for _, other := range enabled {
				if other.Name != c {
					continue
				}
				pair := []string{b.Name, other.Name}
				sort.Strings(pair)
				key := strings.Join(pair, "|")
				if seen[key] {
					continue
				}
				seen[key] = true
				view.Conditions = append(view.Conditions, placementCondition{Kind: "conflict", Blocks: pair,
					Sentence: fmt.Sprintf("%s and %s contradict each other and are both in force — choose one under Guidance", pair[0], pair[1])})
			}
		}
	}
	onForge := false
	for _, pp := range view.Packs {
		if pp.Platform {
			onForge = true
		}
	}
	if p.DefaultBranch != "" && !onForge {
		view.Conditions = append(view.Conditions, placementCondition{Kind: "no-platform",
			Sentence: "this project is not on the forge pack: one of the platform blocks is missing from its guidance"})
	}
	drift := ""
	state, err := h.adoption.Current(ctx, p.ID)
	found, err := database.Lookup(err)
	if err != nil {
		return PlacementView{}, err
	}
	if found && state != nil {
		drift = state.Drift
	}
	if drift != "" {
		view.Conditions = append(view.Conditions, placementCondition{Kind: "drift",
			Sentence: "the guidance or lanes moved outside the forge's own door (" + drift + "); gates and steering still read revision " + fmt.Sprint(ad.Revision) + " — save under Guidance to record what the project should be on"})
	}
	if ad.Receipt.Pack != "" && ad.Receipt.Pack != "forge" {
		placed := false
		for _, pp := range view.Packs {
			if pp.Pack == ad.Receipt.Pack {
				placed = true
			}
		}
		if !placed {
			view.Conditions = append(view.Conditions, placementCondition{Kind: "unplaced",
				Sentence: fmt.Sprintf("the project's record names the %s pack, but no published version of it matches what the project is on; its blocks are carried as additions with unknown provenance", ad.Receipt.Pack)})
		}
	}
	return view, nil
}
