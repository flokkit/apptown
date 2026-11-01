package wow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
)

// Pack resolution: a publication stores the pack's own items with the
// library's current pins, so adoption, conformance and the fleet read
// exactly what a version holds whatever the library publishes since. Packs
// do not include one another; a project combines several (Combine).

// errUnpinned refuses a pack version that names an item the library does
// not have: it could not pin it, and a later item of that name would take
// its place in every project on the version.
var errUnpinned = fault.New(fault.Invalid, "a pack version names only items it can pin")

// pinSource is what resolution needs from the library: an item's current
// pin. ok is false when the library lacks it; pinned is false for a
// workflow the pack names but does not pin (a system workflow of a
// non-platform pack).
type pinSource interface {
	BlockPin(ctx context.Context, name string) (version int, ok bool)
	WorkflowPin(ctx context.Context, name string, platform bool) (version int, pinned, ok bool)
	// PolicyPin is a library policy's current definition.
	PolicyPin(ctx context.Context, name string) (PackPolicy, bool)
}

// resolution is a resolved declaration: the contents and pins a version
// stores.
type resolution struct {
	Spec PackSpec
	Pins PinSet
	// Missing names the pack's items the library does not have, which the
	// publication could not pin. A person's save or a stock sync is refused
	// for them (a version pins what it names); a recut, made for the pack at
	// boot, keeps them by name as before.
	Missing []string
}

// resolveDeclaration is the one resolution rule, deterministic for equal
// inputs: it pins the pack's items at the library's current versions, and
// stores each policy with the definition it pins.
func resolveDeclaration(ctx context.Context, d PackSpec, lib pinSource) resolution {
	pins := PinSet{Blocks: map[string]int{}, Workflows: map[string]int{}, Policies: map[string]string{}}
	var missing []string
	// A policy is stored with the library's definition now, pinned by it;
	// one the library lacks keeps its name, unpinned (unpinnedPolicies).
	policies := make([]PackPolicy, 0, len(d.Policies))
	for _, p := range d.Policies {
		def, ok := lib.PolicyPin(ctx, p.Name)
		if !ok {
			policies = append(policies, PackPolicy{Name: p.Name})
			continue
		}
		pins.Policies[p.Name] = policyPin(def)
		policies = append(policies, def)
	}
	d.Policies = policies
	for _, g := range d.Guidance {
		if v, ok := lib.BlockPin(ctx, g); ok {
			pins.Blocks[g] = v
		} else {
			missing = append(missing, "guidance block "+g)
		}
	}
	// A system workflow a language pack lists is known but left for the
	// platform pack to pin; only an unknown one is missing.
	for _, w := range d.Workflows {
		v, pinned, ok := lib.WorkflowPin(ctx, w, d.Platform)
		switch {
		case !ok:
			missing = append(missing, "workflow "+w)
		case pinned:
			pins.Workflows[w] = v
		}
	}
	return resolution{Spec: d, Pins: pins, Missing: append(missing, unpinnedPolicies(d.Policies, pins)...)}
}

// unpinnedPolicies names the policies a resolved pack holds with neither a
// pin nor a definition: judged on the final contents, so one the pack
// dropped or replaced is not counted.
func unpinnedPolicies(policies []PackPolicy, pins PinSet) []string {
	var out []string
	for _, p := range policies {
		if _, pinned := pins.Policies[p.Name]; !pinned && p.Rule == "" {
			out = append(out, "policy "+p.Name)
		}
	}
	return out
}

func decodeSpec(raw string) (PackSpec, error) {
	var s PackSpec
	if strings.TrimSpace(raw) == "" {
		return s, nil
	}
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// Publication is one pack version as recorded: the author's declaration,
// and its pinned contents.
type Publication struct {
	Version     int64    `json:"version"`
	Name        string   `json:"name,omitempty"`
	BasedOn     int64    `json:"basedOn,omitempty"`
	Declaration PackSpec `json:"declaration"`
	Spec        PackSpec `json:"spec"`
	Pins        PinSet   `json:"pins"`
	Note        string   `json:"note"`
	// Origin is where the version came from (origin.go).
	Origin
	At stamp.Time `json:"at"`
}

// publicationsOf is a pack's versions as recorded, newest first.
func (s *packs) publicationsOf(ctx context.Context, name string) ([]Publication, error) {
	q := wowsql.New(s.db.SQL())
	p, err := packByNameIn(ctx, q, name)
	if err != nil {
		return nil, err
	}
	rows, err := q.PackVersions(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Publication, 0, len(rows))
	for _, r := range rows {
		pub := Publication{Version: r.Version, Name: r.Name, BasedOn: r.BasedOn, Note: r.Note, Origin: Origin{Source: r.Source, Signer: r.Signer},
			At: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}}}
		// A publication that cannot be read is an error, not an empty one:
		// the list and the recommendation show what each version holds.
		var err error
		if pub.Declaration, err = decodeSpec(string(r.DeclarationJson)); err != nil {
			return nil, fmt.Errorf("%s v%d's declaration could not be read: %w", name, r.Version, err)
		}
		if pub.Spec, err = decodeSpec(string(r.SpecJson)); err != nil {
			return nil, fmt.Errorf("%s v%d's resolved spec could not be read: %w", name, r.Version, err)
		}
		if len(bytes.TrimSpace(r.PinsJson)) == 0 {
			// recorded without pins: none
		} else if err := json.Unmarshal(r.PinsJson, &pub.Pins); err != nil {
			return nil, fmt.Errorf("%s v%d's pins could not be read: %w", name, r.Version, err)
		}
		out = append(out, pub)
	}
	return out, nil
}
