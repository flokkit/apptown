package wow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"slices"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
)

// Pack inclusion (design/subsystems/ways-of-working-versions.md). A pack may
// be another pack with some items replaced or dropped and its own items
// added. Every publication stores the resolved result, so adoption,
// conformance and the fleet read a flat pack. Items are guidance blocks,
// workflows and policies, by name. An inherited item always keeps the pin of
// the included publication, whatever the library has published since; only
// the pack's own items (additions and replacements) are pinned at the
// library's current versions.

// ErrInclusion is a declaration that cannot be resolved.
var ErrInclusion = fault.New(fault.Invalid, "the pack's inclusion cannot be resolved")

// ErrUnpinned refuses a pack version that names an item the library does
// not have: it could not pin it, and a later item of that name would take
// its place in every project on the version.
var ErrUnpinned = fault.New(fault.Invalid, "a pack version names only items it can pin")

// IncludeRef splits "name" or "name@4". Version 0 follows the included
// pack's offered version.
func IncludeRef(includes string) (name string, version int64, err error) {
	name, v, pinned := strings.Cut(strings.TrimSpace(includes), "@")
	if !pinned {
		return name, 0, nil
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil || n < 1 {
		return "", 0, fmt.Errorf("%w: %q: a pinned include is name@N with N at least 1", ErrInclusion, includes)
	}
	return name, n, nil
}

// includedPublication is the exact publication a pack resolves against.
type includedPublication struct {
	Pack    string
	Version int64
	Spec    PackSpec
	Pins    PinSet
}

// pinSource is what resolution needs from the library: an own item's
// current pin. ok is false when the library lacks it; pinned is false for
// a workflow the pack names but does not pin (a system workflow of a
// non-platform pack).
type pinSource interface {
	BlockPin(ctx context.Context, name string) (version int, ok bool)
	WorkflowPin(ctx context.Context, name string, platform bool) (version int, pinned, ok bool)
	// PolicyPin is a library policy's current definition.
	PolicyPin(ctx context.Context, name string) (PackPolicy, bool)
}

// resolution is a resolved declaration: the contents and pins a version
// stores, the publication it included, and the replaced or dropped items
// the included publication no longer has.
type resolution struct {
	Spec     PackSpec
	Pins     PinSet
	Included *includedPublication
	// Gone names replaced or dropped items the included publication does
	// not carry. Saving a declaration refuses them; a cut made for the pack
	// records them in its note.
	Gone []string
	// Missing names the pack's own items the library does not have, which
	// the publication could not pin. A person's save or a stock sync is
	// refused for them (a version pins what it names); a recut, made for
	// the pack at boot, keeps them by name as before.
	Missing []string
}

// resolveDeclaration is the one resolution rule: deterministic for equal
// inputs. With no include it pins the pack's own items, as before
// inclusion; with one it starts from the included publication, applies
// drops, then replacements, then adds the pack's own items.
func resolveDeclaration(ctx context.Context, d PackSpec, inc *includedPublication, lib pinSource) (resolution, error) {
	pins := PinSet{Blocks: map[string]int{}, Workflows: map[string]int{}, Policies: map[string]string{}}
	var missing []string
	// An own policy is stored with the library's definition now, pinned by
	// it; one the library lacks keeps its name, unpinned (unpinnedPolicies).
	own := func(name string) PackPolicy {
		def, ok := lib.PolicyPin(ctx, name)
		if !ok {
			return PackPolicy{Name: name}
		}
		pins.Policies[name] = policyPin(def)
		return def
	}
	if d.Includes == "" {
		policies := make([]PackPolicy, 0, len(d.Policies))
		for _, p := range d.Policies {
			policies = append(policies, own(p.Name))
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
		return resolution{Spec: d, Pins: pins, Missing: append(missing, unpinnedPolicies(d.Policies, pins)...)}, nil
	}
	if inc == nil {
		return resolution{}, fmt.Errorf("%w: %s has no publication to include", ErrInclusion, d.Includes)
	}
	if d.Platform || inc.Spec.Platform {
		return resolution{}, fmt.Errorf("%w: the platform pack neither includes nor is included; every repo-backed project is on it", ErrInclusion)
	}
	out := PackSpec{
		Guidance:   slices.Clone(inc.Spec.Guidance),
		Workflows:  slices.Clone(inc.Spec.Workflows),
		Policies:   slices.Clone(inc.Spec.Policies),
		Resources:  inc.Spec.Resources,
		Toolchain:  inc.Spec.Toolchain,
		Services:   slices.Clone(inc.Spec.Services),
		Extensions: slices.Clone(inc.Spec.Extensions),
		Requires:   slices.Clone(inc.Spec.Requires),
		Starter:    inc.Spec.Starter,
	}
	if out.Starter == "" {
		out.Starter = inc.Pack
	}
	for _, g := range out.Guidance {
		if v, ok := inc.Pins.Blocks[g]; ok {
			pins.Blocks[g] = v
		}
	}
	for _, w := range out.Workflows {
		if v, ok := inc.Pins.Workflows[w]; ok {
			pins.Workflows[w] = v
		}
	}
	// An inherited policy keeps the included publication's definition
	// exactly, pinned by it (or by the definition, where it holds no pin).
	// One it only named, because the library lacked it then, is pinned here
	// at the library's current definition if it has one now.
	for i, p := range out.Policies {
		switch v, pinned := inc.Pins.Policies[p.Name]; {
		case pinned:
			pins.Policies[p.Name] = v
		case p.Rule != "":
			pins.Policies[p.Name] = policyPin(p)
		default:
			out.Policies[i] = own(p.Name)
		}
	}
	var gone []string
	for _, drop := range d.Drops {
		if !removeItem(&out, &pins, drop.Item) {
			gone = append(gone, drop.Item)
		}
	}
	// Replacements in the declaration's map, applied in the order of the
	// included lists so the result does not depend on map iteration. A
	// replacement that is itself in the pack (inherited, or another
	// replacement's) is refused: taking it would repin an inherited item at
	// the library's version, or list one item twice.
	replaced := map[string]bool{}
	taken := func(list []string, to string) error {
		if slices.Contains(list, to) {
			return fmt.Errorf("%w: %q is already in the pack, so nothing can be replaced with it; drop one of them instead", ErrInclusion, to)
		}
		return nil
	}
	for i, g := range out.Guidance {
		if to, ok := d.Replaces[g]; ok {
			if err := taken(out.Guidance, to); err != nil {
				return resolution{}, err
			}
			v, known := lib.BlockPin(ctx, to)
			if !known {
				return resolution{}, fmt.Errorf("%w: replacement block %q is not in the library", ErrInclusion, to)
			}
			delete(pins.Blocks, g)
			out.Guidance[i], pins.Blocks[to], replaced[g] = to, v, true
		}
	}
	for i, w := range out.Workflows {
		if to, ok := d.Replaces[w]; ok {
			if err := taken(out.Workflows, to); err != nil {
				return resolution{}, err
			}
			v, pinned, known := lib.WorkflowPin(ctx, to, false)
			if !known {
				return resolution{}, fmt.Errorf("%w: replacement workflow %q is not in the library", ErrInclusion, to)
			}
			delete(pins.Workflows, w)
			out.Workflows[i], replaced[w] = to, true
			if pinned {
				pins.Workflows[to] = v
			}
		}
	}
	for i, p := range out.Policies {
		if to, ok := d.Replaces[p.Name]; ok {
			names := make([]string, 0, len(out.Policies))
			for _, q := range out.Policies {
				names = append(names, q.Name)
			}
			if err := taken(names, to); err != nil {
				return resolution{}, err
			}
			if _, known := lib.PolicyPin(ctx, to); !known {
				return resolution{}, fmt.Errorf("%w: replacement policy %q is not in the policy library", ErrInclusion, to)
			}
			delete(pins.Policies, p.Name)
			out.Policies[i], replaced[p.Name] = own(to), true
		}
	}
	for _, from := range sortedKeys(d.Replaces) {
		if !replaced[from] {
			gone = append(gone, from)
		}
	}
	for _, g := range d.Guidance {
		if slices.Contains(out.Guidance, g) {
			return resolution{}, fmt.Errorf("%w: block %q is already included; replace it instead", ErrInclusion, g)
		}
		v, known := lib.BlockPin(ctx, g)
		if !known {
			return resolution{}, fmt.Errorf("%w: block %q is not in the library", ErrInclusion, g)
		}
		out.Guidance, pins.Blocks[g] = append(out.Guidance, g), v
	}
	for _, w := range d.Workflows {
		if slices.Contains(out.Workflows, w) {
			return resolution{}, fmt.Errorf("%w: workflow %q is already included; replace it instead", ErrInclusion, w)
		}
		v, pinned, known := lib.WorkflowPin(ctx, w, false)
		if !known {
			return resolution{}, fmt.Errorf("%w: workflow %q is not in the library", ErrInclusion, w)
		}
		out.Workflows = append(out.Workflows, w)
		if pinned {
			pins.Workflows[w] = v
		}
	}
	for _, p := range d.Policies {
		if slices.ContainsFunc(out.Policies, func(q PackPolicy) bool { return q.Name == p.Name }) {
			return resolution{}, fmt.Errorf("%w: policy %q is already included; replace it instead", ErrInclusion, p.Name)
		}
		out.Policies = append(out.Policies, own(p.Name))
	}
	if d.Resources.Memory != "" {
		out.Resources = d.Resources
	}
	if d.Toolchain != "" {
		out.Toolchain = d.Toolchain
	}
	if len(d.Services) > 0 {
		out.Services = slices.Clone(d.Services)
	}
	for _, field := range d.Clears {
		switch field {
		case "resources":
			out.Resources = appcontract.MemoryDefault{}
		case "toolchain":
			out.Toolchain = ""
		case "services":
			out.Services = nil
		default:
			return resolution{}, fmt.Errorf("%w: clears names %q; only toolchain, services and resources are inherited", ErrInclusion, field)
		}
	}
	for _, e := range d.Extensions {
		if !slices.Contains(out.Extensions, e) {
			out.Extensions = append(out.Extensions, e)
		}
	}
	for _, c := range d.Requires {
		if !slices.Contains(out.Requires, c) {
			out.Requires = append(out.Requires, c)
		}
	}
	return resolution{Spec: out, Pins: pins, Included: inc, Gone: gone, Missing: unpinnedPolicies(out.Policies, pins)}, nil
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

// removeItem drops a named block, workflow or policy from a resolved pack.
func removeItem(spec *PackSpec, pins *PinSet, item string) bool {
	if i := slices.Index(spec.Guidance, item); i >= 0 {
		spec.Guidance = slices.Delete(spec.Guidance, i, i+1)
		delete(pins.Blocks, item)
		return true
	}
	if i := slices.Index(spec.Workflows, item); i >= 0 {
		spec.Workflows = slices.Delete(spec.Workflows, i, i+1)
		delete(pins.Workflows, item)
		return true
	}
	if i := slices.IndexFunc(spec.Policies, func(p PackPolicy) bool { return p.Name == item }); i >= 0 {
		spec.Policies = slices.Delete(spec.Policies, i, i+1)
		delete(pins.Policies, item)
		return true
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
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
// its resolved contents and pins, and the publication it included.
type Publication struct {
	Version         int64      `json:"version"`
	Name            string     `json:"name,omitempty"`
	BasedOn         int64      `json:"basedOn,omitempty"`
	Declaration     PackSpec   `json:"declaration"`
	Spec            PackSpec   `json:"spec"`
	Pins            PinSet     `json:"pins"`
	IncludedPack    string     `json:"includedPack,omitempty"`
	IncludedVersion int64      `json:"includedVersion,omitempty"`
	Note            string     `json:"note"`
	At              stamp.Time `json:"at"`
}

// PublicationsOf is a pack's versions as recorded, newest first.
func (s *Packs) PublicationsOf(ctx context.Context, name string) ([]Publication, error) {
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
		pub := Publication{Version: r.Version, Name: r.Name, BasedOn: r.BasedOn, IncludedPack: r.IncludedPack, IncludedVersion: r.IncludedVersion, Note: r.Note,
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
