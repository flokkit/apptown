package wow

import (
	"context"
	"fmt"
	"log"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/strs"
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

// PinsFor pins a pack spec's own blocks and workflows at the library's
// latest versions, as canonical JSON. Anything the library lacks is left
// out; prepare says so. System workflows (land, ship, …) are the platform
// pack's to pin: a language pack lists them but does not pin them, or
// every pack would cut a version each time the landing moved and disagree
// with the platform about it. Included items are not its business: their
// pins come from the included publication (resolveDeclaration).
func (s *Packs) PinsFor(ctx context.Context, spec string) (string, error) {
	d, _ := decodeSpec(spec)
	d.Includes = ""
	res, _ := resolveDeclaration(ctx, d, nil, libraryPins{lib: NewLibrary(s.db), wf: NewWorkflows(s.db), pol: NewPolicies(s.db)})
	pins, err := strs.JSON(res.Pins)
	return canonJSON(pins), err
}

// Recut names a pack that got a new version because its contents moved.
type Recut struct {
	Name    string
	Version int
}

// CutOnPinChange publishes a new version of every pack whose own items
// moved in the library. The declaration is resolved against the same
// included publication its latest version recorded, so an inherited item
// never takes a library version its included publication does not name. A
// pack whose publication cannot be read or republished is logged and
// skipped, never fatal at boot.
func (s *Packs) CutOnPinChange(ctx context.Context) ([]Recut, error) {
	packs, err := s.Published(ctx)
	if err != nil {
		return nil, err
	}
	var out []Recut
	for _, r := range packs {
		var next int64
		err := s.db.InTx(ctx, func(tx *database.Tx) error {
			// The pack as it is now, under its lock: an earlier cut in this
			// sweep may have published a follow version of it, which this
			// cut starts from and must never undo.
			q := wowsql.New(tx.SQL())
			p, err := q.PackByNameForUpdate(ctx, r.Name)
			if err != nil {
				return err
			}
			latest, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: p.Name, Version: p.LatestVersion})
			if err != nil || latest.ID.IsZero() {
				return fmt.Errorf("reading its publication: %w", err)
			}
			res, err := s.resolveIn(ctx, q, p.Name, string(p.SpecJson), latest.IncludedVersion)
			if err != nil {
				return err
			}
			pins, err := strs.JSON(res.Pins)
			if err != nil {
				return err
			}
			if canonJSON(string(latest.PinsJson)) == canonJSON(pins) {
				return nil
			}
			next, err = s.publishIn(ctx, tx, s.now(), p.Name, p.Description, string(p.SpecJson), p.Origin, cut{includedVersion: latest.IncludedVersion, note: "its own items moved in the library"})
			return err
		})
		if err != nil {
			log.Printf("pack %s: publish changed contents: %v", r.Name, err)
			continue
		}
		if next != 0 {
			log.Printf("pack %q: now v%d (its contents moved)", r.Name, next)
			out = append(out, Recut{Name: r.Name, Version: int(next)})
		}
	}
	return out, nil
}
