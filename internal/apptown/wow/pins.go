package wow

import (
	"context"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// A pack publication names exact contents (ways-of-working-versions.md
// §11): each guidance block and each workflow at a version. Pinning is the
// rule that says which versions a publication names, and cutting on pin
// change is the rule that a pack whose list is unchanged but whose blocks
// or workflows moved in the library gets a new version, since new
// contents are a new publication. Projects stay where they are until they
// adopt it.

// PinSet is the exact contents of a publication: block name → version,
// workflow name → library version, policy name → the definition the
// publication stores (canonical JSON of its description, rule and
// options). The policy library is not versioned, so a policy is pinned by
// its definition: a release or an edit that changes one is a pin change,
// and the next sweep publishes it.
type PinSet struct {
	Blocks    map[string]int    `json:"blocks"`
	Workflows map[string]int    `json:"workflows"`
	Policies  map[string]string `json:"policies,omitempty"`
}

// policyPin is the pin of a policy definition.
func policyPin(p PackPolicy) string {
	s, _ := strs.JSON(PackPolicy{Name: p.Name, Description: p.Description, Rule: p.Rule, With: p.With})
	return canonJSON(s)
}

// recut names a pack that got a new version because its contents moved.
type recut struct {
	Name    string
	Version int
}

// CutOnPinChange publishes a new version of every pack whose items moved
// in the library. A pack whose publication cannot be read or republished
// is logged and skipped, never fatal at boot.
func (s *packs) CutOnPinChange(ctx context.Context) ([]recut, error) {
	packs, err := s.published(ctx)
	if err != nil {
		return nil, err
	}
	var out []recut
	for _, r := range packs {
		var next int64
		err := s.db.InTx(ctx, func(tx *database.Tx) error {
			// The pack as it is now, under its lock.
			q := wowsql.New(tx.SQL())
			p, err := q.PackByNameForUpdate(ctx, r.Name)
			if err != nil {
				return err
			}
			latest, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: p.Name, Version: p.LatestVersion})
			if err != nil || latest.ID.IsZero() {
				return fmt.Errorf("reading its publication: %w", err)
			}
			pins, err := strs.JSON(s.resolve(ctx, string(p.SpecJson)).Pins)
			if err != nil {
				return err
			}
			if canonJSON(string(latest.PinsJson)) == canonJSON(pins) {
				return nil
			}
			next, err = s.publishIn(ctx, tx, s.now(), p.Name, p.Description, string(p.SpecJson), p.Origin, cut{note: "its items moved in the library"})
			return err
		})
		if err != nil {
			log.Printf("pack %s: publish changed contents: %v", r.Name, err)
			continue
		}
		if next != 0 {
			log.Printf("pack %q: now v%d (its contents moved)", r.Name, next)
			out = append(out, recut{Name: r.Name, Version: int(next)})
		}
	}
	return out, nil
}
