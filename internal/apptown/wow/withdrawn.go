package wow

import (
	"context"
	"log"
	"sync"
)

// Withdrawn stock: a stored entry that came from stock and that the
// running release no longer ships. It is no longer offered — the guidance,
// workflow and policy libraries leave it out — but it is never deleted: a
// project's adopted guidance points at its versions, a selected workflow is
// resolved from the library, and runs keep what they ran. Where a project
// uses one, its reads say so. People cannot edit a stock entry (they clone
// it), so a stock entry is always the release's own; custom entries are
// never withdrawn. A stock pack the release no longer ships is turned off
// at sync instead (SyncStockPacks): turning a pack off is already what
// stops new projects taking it.

// Kind names which library a stock name belongs to.
type Kind int

const (
	KindWorkflow Kind = iota
	KindGuidance
	kindPolicy
	kindPack
)

// shippedNames is the names the release ships, by kind. A nil set is a kind
// whose stock did not all read (or a service with no stock): nothing of
// that kind is withdrawn, so one bad file never hides the rest.
type shippedNames map[Kind]map[string]bool

// shippedOnce reads the release's stock names once per service.
type shippedOnce struct {
	once  sync.Once
	names shippedNames
}

func (s *Service) shipped() shippedNames {
	s.shippedNames.once.Do(func() { s.shippedNames.names = readShipped(s.deps) })
	return s.shippedNames.names
}

func readShipped(deps Dependencies) shippedNames {
	out := shippedNames{}
	if deps.Stock == nil {
		return out
	}
	set := func(k Kind, names []string, complete bool) {
		if !complete {
			log.Printf("stock %s names: not every file read, so none is treated as withdrawn", k)
			return
		}
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		out[k] = m
	}
	workflows, skipped := readStockWorkflows(deps.Stock)
	names := make([]string, 0, len(workflows))
	for _, w := range workflows {
		names = append(names, w.Name)
	}
	set(KindWorkflow, names, skipped == 0)

	blocks, skipped := ReadStockGuidance(deps.Stock, nil)
	names = make([]string, 0, len(blocks))
	for _, b := range blocks {
		names = append(names, b.Name)
	}
	set(KindGuidance, names, skipped == 0)

	policies, err := readStockPolicies(deps.Stock)
	names = make([]string, 0, len(policies))
	for _, p := range policies {
		names = append(names, p.Name)
	}
	set(kindPolicy, names, err == nil)

	packs, err := ReadStockPacks(deps.Stock)
	names = make([]string, 0, len(packs))
	for _, p := range packs {
		names = append(names, p.Name)
	}
	set(kindPack, names, err == nil)
	return out
}

func (k Kind) String() string {
	switch k {
	case KindWorkflow:
		return "workflow"
	case KindGuidance:
		return "guidance"
	case kindPolicy:
		return "policy"
	case kindPack:
		return "pack"
	}
	return "unknown"
}

// Withdrawn says whether a stored entry of this kind, with this origin and
// name, is stock the running release no longer ships.
func (s *Service) Withdrawn(k Kind, origin, name string) bool {
	if origin != "stock" {
		return false
	}
	set := s.shipped()[k]
	return set != nil && !set[name]
}

// withdrawUnshippedPacks turns off each stock pack the release no longer
// ships, so new projects are not offered it; the projects on it stay on it.
func (s *Service) withdrawUnshippedPacks(ctx context.Context) {
	all, err := s.packs.all(ctx)
	if err != nil {
		log.Printf("stock packs: reading which are no longer shipped: %v", err)
		return
	}
	for _, p := range all {
		if !p.Enabled || !s.Withdrawn(kindPack, p.Origin, p.Name) {
			continue
		}
		if err := s.packs.setEnabled(ctx, p.Name, false, "apptown-system"); err != nil {
			log.Printf("stock pack %s: no longer shipped, but turning it off failed: %v", p.Name, err)
			continue
		}
		log.Printf("stock pack %s: no longer shipped, so turned off; projects on it stay on it", p.Name)
	}
}
