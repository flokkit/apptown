package adoption

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// Pack pull: the PACK is the unit of adoption. A
// project records which pack it took; when the pack evolves, the project
// opts into the new shape by PULLING it — additions install, removals
// remove, in one visible act. Nothing propagates as a side effect of
// running work: a worker-level act must not mutate the rule surface.
//
// The plan reports availability changes from pack membership. Library workflows
// remain available independently of packs.

type PullPlan struct {
	Pack      string   `json:"pack"`
	Additions []string `json:"additions"` // made available by the new pack
	Removals  []string `json:"removals"`  // no longer supplied by the adopted packs
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
		Guidance: []string{}, Conflicting: []string{}}
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
	before, err := packWorkflowNamesIn(ctx, s.db, projectID)
	if err != nil {
		return plan, err
	}
	after := map[string]bool{}
	memberships, err := ProjectPacksIn(ctx, s.db, projectID)
	if err != nil {
		return plan, err
	}
	for _, m := range memberships {
		if m.Pack == pack.Name {
			continue
		}
		var spec candidatePackSpec
		if err := json.Unmarshal([]byte(m.SpecJSON), &spec); err != nil {
			return plan, fmt.Errorf("pack %s: %w", m.Pack, err)
		}
		for _, name := range spec.Workflows {
			after[name] = true
		}
	}
	for _, name := range pack.Workflows {
		after[name] = true
	}
	for _, entry := range entries {
		if entry.Doc.Metadata.System || !entry.Withdrawn {
			before[entry.Name], after[entry.Name] = true, true
		}
	}
	for name := range after {
		if !before[name] {
			plan.Additions = append(plan.Additions, name)
		}
	}
	for name := range before {
		if !after[name] {
			plan.Removals = append(plan.Removals, name)
		}
	}
	sort.Strings(plan.Additions)
	sort.Strings(plan.Removals)

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
	Installed []string // workflows newly available
	Removed   []string // workflows no longer available; every run that used one keeps its receipt
}

// PullPack moves the pack membership and records its adoption revision together.
// Guidance additions remain explicit edits. within settles other owners' effects
// in the membership transaction.
func (s *Service) PullPack(ctx context.Context, projectID ids.ID, pack wow.Pack, who string, within func(*database.Tx) error) (Pulled, error) {
	plan, err := s.PackPullPlan(ctx, projectID, pack)
	if err != nil {
		return Pulled{}, err
	}
	// The packs the pull leaves the project on are judged as a whole, as any
	// adoption is: held packs at their versions, this one at its latest.
	picks, err := s.PicksWith(ctx, projectID, pack.Name, pack.LatestVersion)
	if err != nil {
		return Pulled{}, err
	}
	current, err := s.CurrentReceipt(ctx, projectID)
	if found, err := database.Lookup(err); err != nil {
		return Pulled{}, err
	} else if !found || current == nil {
		current = &RecordedRevision{Receipt: EmptyReceipt()}
	}
	memberships, err := ProjectPacksIn(ctx, s.db, projectID)
	if err != nil {
		return Pulled{}, err
	}
	whole, err := (candidateBuilder{ways: s.deps.Ways}).prepare(ctx, projectID, current, memberships, Request{Packs: picks})
	if err != nil {
		return Pulled{}, err
	}
	if len(whole.Problems) != 0 {
		return Pulled{}, fmt.Errorf("%w: %s", ErrNotAdoptable, strings.Join(whole.Problems, "; "))
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
	// The pack and its revision commit together,
	// so no pull lands without its revision.
	if _, err := s.Advance(ctx, projectID, AdoptionEdit, "pack "+pack.Name+" pulled (lanes)", who, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		// A pull selects this publication explicitly, in the same transaction
		// as its receipt; the project's other packs stay as they are.
		if pack.LatestVersion <= 0 {
			return Receipt{}, fmt.Errorf("pulled pack %s has no selected publication", pack.Name)
		}
		if _, err := wow.PackVersionIn(ctx, tx, pack.Name, pack.LatestVersion); err != nil {
			return Receipt{}, fmt.Errorf("reading the pulled publication: %w", err)
		}
		if err := replacePulledIn(ctx, tx, projectID, pack, who, now); err != nil {
			return Receipt{}, err
		}
		if err := within(tx); err != nil {
			return Receipt{}, err
		}
		return s.UnionIn(ctx, tx, projectID, prior)
	}); err != nil {
		return Pulled{}, err
	}
	return out, nil
}
