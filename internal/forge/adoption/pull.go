package adoption

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"sort"
	"strings"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/wow"
)

// Pack pull: the PACK is the unit of adoption. A
// project records which pack it took; when the pack evolves, the project
// opts into the new shape by PULLING it — additions install, removals
// remove, in one visible act. Nothing propagates as a side effect of
// running work: a worker-level act must not mutate the rule surface.
//
// Two phases so removals are seen before they happen: the plan answers
// "what would change", the pull does it. Since workflows-as-lanes a
// project selects lanes and copies nothing, so a pull is a change of
// selection: nothing of the project's own is ever overwritten.

type PullPlan struct {
	Pack      string   `json:"pack"`
	Additions []string `json:"additions"` // in the pack, not selected by the project
	Removals  []string `json:"removals"`  // selected, no longer in the pack, nothing left calls it
	// KeptReferenced: the pack dropped it, but a surviving workflow still
	// CALLS it (workflow/call) — removing it would break a landing at the
	// call site. Kept, loudly.
	KeptReferenced []string `json:"keptReferenced"`
	Deprecated     []string `json:"deprecated"` // in the pack but deprecated in the library — not installed
	// Guidance: blocks the pack now carries that the project does not
	// import — a way of working the pack adopted since the project took it.
	// Listed for a person to add under Guidance; the project's own imports
	// and its pins are never touched.
	Guidance []string `json:"guidance"`
	// Conflicting: blocks the pack carries that contradict one the project
	// pins (a block's `conflicts:`). Not adopted; a person chooses. The
	// sentence says which pinned block stands in the way.
	Conflicting []string `json:"conflicting"`
}

// PackPullPlan computes what pulling the pack would do to the project.
func (s *Service) PackPullPlan(ctx context.Context, projectID ids.ID, pack wow.Pack) (PullPlan, error) {
	plan := PullPlan{Pack: pack.Name, Additions: []string{}, Removals: []string{},
		KeptReferenced: []string{}, Deprecated: []string{}, Guidance: []string{}, Conflicting: []string{}}
	imports, err := importsIn(ctx, s.db, projectID)
	if err != nil {
		return plan, err
	}
	imported := map[string]bool{}
	pinned := map[string]bool{}            // imported and enabled
	pinnedConflicts := map[string]string{} // block name → the pinned block that names it
	for _, im := range imports {
		imported[im.Name] = true
		if !im.Disabled {
			pinned[im.Name] = true
			for _, c := range strings.Split(im.Conflicts, ",") {
				if c = strings.TrimSpace(c); c != "" {
					pinnedConflicts[c] = im.Name
				}
			}
		}
	}
	for _, g := range pack.Guidance {
		if imported[g] {
			continue
		}
		if why := s.guidanceConflict(ctx, g, pinned, pinnedConflicts); why != "" {
			plan.Conflicting = append(plan.Conflicting, why)
			continue
		}
		plan.Guidance = append(plan.Guidance, g)
	}

	entries, err := s.deps.Ways.Workflows(ctx)
	if err != nil {
		return plan, err
	}
	library := map[string]wow.WorkflowEntry{} // the library's latest, by name
	system := map[string]bool{}
	have := map[string]string{} // selected or system: name → the library's latest YAML
	for _, e := range entries {
		library[e.Name] = e
		system[e.Name] = e.Doc.Metadata.System
		selected := e.Doc.Metadata.System
		if !selected {
			if selected, err = s.lanes.Selected(ctx, projectID, e.Name); err != nil {
				return plan, err
			}
		}
		if selected {
			have[e.Name] = e.YAML
		}
	}
	inPack := map[string]bool{}
	for _, w := range pack.Workflows {
		inPack[w] = true
	}
	for _, w := range pack.Workflows {
		if _, ok := have[w]; ok {
			continue // already selected: the library keeps it current
		}
		if e, ok := library[w]; ok {
			deprecated := e.Doc.Metadata.Deprecated //lint:ignore SA1019 reading the deprecated flag is the point
			if deprecated {
				plan.Deprecated = append(plan.Deprecated, w)
				continue
			}
		}
		plan.Additions = append(plan.Additions, w)
	}
	referenced := func(name string) bool {
		for other, yamlSrc := range have {
			if other == name || (!inPack[other] && !system[other]) {
				continue // itself, or a fellow removal candidate
			}
			if strings.Contains(yamlSrc, "workflow: "+name) {
				return true
			}
		}
		for _, w := range pack.Workflows {
			if _, present := have[w]; !present {
				// An incoming addition calls it: read as stored, whether or
				// not the library could parse it into an entry.
				if obs, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: w}); err == nil && strings.Contains(obs.YAML, "workflow: "+name) {
					return true
				}
			}
		}
		return false
	}
	// A selected lane the pack no longer carries is deselected unless
	// something that stays still calls it.
	names := make([]string, 0, len(have))
	for name := range have {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if inPack[name] || system[name] {
			continue // pack still carries it, or the forge runs it regardless
		}
		if referenced(name) {
			plan.KeptReferenced = append(plan.KeptReferenced, name)
		} else {
			plan.Removals = append(plan.Removals, name)
		}
	}
	return plan, nil
}

// guidanceConflict says why a block cannot be adopted beside what the
// project pins: it names a pinned block in its conflicts, or a pinned
// block names it. "" when it can.
func (s *Service) guidanceConflict(ctx context.Context, name string, pinned map[string]bool, pinnedConflicts map[string]string) string {
	if by := pinnedConflicts[name]; by != "" {
		return fmt.Sprintf("%s — contradicts %s, which this project pins; choose one under Guidance", name, by)
	}
	var conflicts string
	if b, err := s.deps.Ways.Block(ctx, wow.BlockRef{Name: name}); err == nil {
		conflicts = b.Block.Conflicts
	}
	for _, c := range strings.Split(conflicts, ",") {
		if c = strings.TrimSpace(c); c != "" && pinned[c] {
			return fmt.Sprintf("%s — contradicts %s, which this project pins; choose one under Guidance", name, c)
		}
	}
	return ""
}

// Pulled is what a pull did.
type Pulled struct {
	Plan      PullPlan
	Installed []string // lanes selected
	Removed   []string // lanes deselected; every run that used one keeps its receipt
}

// PullPack is a person's explicit act: selects the pack's added lanes,
// deselects the removed, records the pack as the project's, and writes the
// edit revision that results. Guidance is not adopted here
// (ways-of-working-versions.md §1.2, §9 step 1): what the pack carries
// that the project lacks is in the plan for a person to add.
func (s *Service) PullPack(ctx context.Context, projectID ids.ID, pack wow.Pack, who string) (Pulled, error) {
	plan, err := s.PackPullPlan(ctx, projectID, pack)
	if err != nil {
		return Pulled{}, err
	}
	out := Pulled{Plan: plan, Installed: []string{}, Removed: []string{}}
	for _, name := range plan.Additions {
		_, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: name})
		if found, err := database.Lookup(err); err != nil {
			return Pulled{}, err
		} else if !found {
			continue // the pack names a lane the library does not hold yet
		}
		out.Installed = append(out.Installed, name)
	}
	out.Removed = append(out.Removed, plan.Removals...)
	// The selections, the pack and the revision they make commit together,
	// so no pull lands without its revision.
	if _, err := s.Advance(ctx, projectID, AdoptionEdit, "pack "+pack.Name+" pulled (lanes)", who, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		for _, name := range out.Installed {
			if err := setLaneIn(ctx, tx, projectID, name, true, now); err != nil {
				return Receipt{}, err
			}
		}
		for _, name := range out.Removed {
			if err := setLaneIn(ctx, tx, projectID, name, false, now); err != nil {
				return Receipt{}, err
			}
		}
		// A pull selects this publication explicitly. Keep the primary pin in
		// the same transaction as the pack name; supplemental picks stay put.
		if pack.LatestVersion <= 0 {
			return Receipt{}, fmt.Errorf("pulled pack %s has no selected publication", pack.Name)
		}
		if _, err := wow.PackVersionIn(ctx, tx, pack.Name, pack.LatestVersion); err != nil {
			return Receipt{}, fmt.Errorf("reading the pulled publication: %w", err)
		}
		if err := replacePulledPrimaryIn(ctx, tx, projectID, pack, who, now); err != nil {
			return Receipt{}, err
		}
		if err := SetPackIn(ctx, tx, projectID, pack.Name); err != nil {
			return Receipt{}, fmt.Errorf("recording the project's pack: %w", err)
		}
		return s.UnionIn(ctx, tx, projectID, prior)
	}); err != nil {
		return Pulled{}, err
	}
	return out, nil
}
