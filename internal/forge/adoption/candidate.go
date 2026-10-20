package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"slices"
	"sort"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/wow"
)

type Request struct {
	RequiredCapabilities []string   `json:"requiredCapabilities,omitempty"`
	Packs                []PackPick `json:"packs"`
	Additions            []string   `json:"additions"`
}

// PlatformPack is the stock platform tier's name (stock/packs.yaml): every
// project with a repository is on it, beside the pack it chose.
const PlatformPack = "apptown"

type PackPick struct {
	Pack    string `json:"pack"`
	Version int64  `json:"version"`
}

// Candidate is a prepared manifest and how it differs from now.
type Installation struct {
	Policies   []Policy
	Extensions []string
}

type Candidate struct {
	applicationSeal      string
	Installation         Installation   `json:"-"`
	ProjectID            ids.ID         `json:"-"`
	RequiredCapabilities []string       `json:"requiredCapabilities,omitempty"`
	ExpectedRevision     int64          `json:"expectedRevision"`
	Packs                []PackPick     `json:"packs"`
	Additions            []string       `json:"additions"`
	Receipt              Receipt        `json:"receipt"`
	Digest               string         `json:"digest"`
	Added                []string       `json:"added"`   // blocks newly in force (name@version)
	Removed              []string       `json:"removed"` // blocks no longer in force
	Changed              []string       `json:"changed"` // name v1→v2
	LanesAdded           []string       `json:"lanesAdded"`
	LanesRemoved         []string       `json:"lanesRemoved"`
	NewChecks            []CheckSummary `json:"newChecks"` // checks the Candidate brings or changes — an uplift must pass these
	Weakens              []string       `json:"weakens"`   // gating blocks a selected pack carries that the Candidate drops: needs the standards authority
	// Kept names workflows and policies the project's current packs supplied
	// that the candidate's packs no longer carry. Adoption keeps a project's
	// workflow selections and installed policies, so these stay after
	// adopting; removing one is a separate project edit
	// (design/subsystems/ways-of-working-versions.md).
	Kept     []string `json:"kept"`
	Problems []string `json:"problems"` // why it cannot be adopted as asked
}

// CheckSummary is the acceptance a newly adopted check contributes to an uplift.
type CheckSummary struct {
	Block   string `json:"block"`
	Check   string `json:"check"`
	Version int    `json:"version,omitempty"`
	Title   string `json:"title,omitempty"`
	Expects string `json:"expects,omitempty"`
}

type candidateBuilder struct{ ways Catalog }

// Prepare describes an exact candidate without changing project state.
func (s *Service) Prepare(ctx context.Context, projectID ids.ID, req Request) (*Candidate, error) {
	if s.deps.Ways == nil {
		return nil, ErrCatalogUnavailable
	}
	current, err := s.CurrentReceipt(ctx, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("the project has no adoption revision yet")
	}
	memberships, err := ProjectPacksIn(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	c, err := (candidateBuilder{ways: s.deps.Ways}).prepare(ctx, projectID, current, memberships, req)
	if err != nil {
		return nil, err
	}
	if len(c.Problems) == 0 {
		state := Adoption{ProjectID: projectID, Revision: current.Revision}
		if _, err := state.Prepare(*c, func(required []string) error {
			return s.requireCapabilities(ctx, s.db, projectID, required)
		}); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (a candidateBuilder) prepare(ctx context.Context, projectID ids.ID, cur *RecordedRevision, memberships []ProjectPackRecord, req Request) (*Candidate, error) {
	view, err := candidatePlacement(cur, memberships)
	if err != nil {
		return nil, err
	}
	c := &Candidate{ProjectID: projectID, RequiredCapabilities: slices.Clone(req.RequiredCapabilities), ExpectedRevision: cur.Revision, Packs: slices.Clone(req.Packs), Additions: slices.Clone(req.Additions),
		Added: []string{}, Removed: []string{}, Changed: []string{}, LanesAdded: []string{}, LanesRemoved: []string{}, NewChecks: []CheckSummary{}, Weakens: []string{}, Problems: []string{}, Kept: []string{}}
	if c.Packs == nil {
		for _, pp := range view.Packs {
			c.Packs = append(c.Packs, PackPick{Pack: pp.Pack, Version: pp.Version})
		}
	}
	if c.Additions == nil {
		c.Additions = []string{}
		for _, b := range view.Additions {
			c.Additions = append(c.Additions, b.Name)
		}
	}
	if c.Packs == nil {
		c.Packs = []PackPick{}
	}
	// Blocks from the packs, exact versions, one entry per block.
	type want struct {
		version int
		from    string
	}
	wanted := map[string]want{}
	var order []string
	lanes := map[string]bool{}
	laneVersion := map[string]int{} // the packs' workflow pins
	carriedPolicies := map[string]bool{}
	for _, pk := range c.Packs {
		version, err := a.packPublication(ctx, pk.Pack, pk.Version)
		if err != nil {
			c.Problems = append(c.Problems, err.Error())
			continue
		}
		ps, pins := version.Spec, version.Pins
		c.RequiredCapabilities = append(c.RequiredCapabilities, ps.Requires...)
		for _, g := range ps.Guidance {
			v, pinned := pins.Blocks[g]
			if !pinned {
				c.Problems = append(c.Problems, fmt.Sprintf("%s v%d does not pin a version of %s", pk.Pack, pk.Version, g))
				continue
			}
			if w, seen := wanted[g]; seen {
				if w.version != v {
					c.Problems = append(c.Problems, fmt.Sprintf("%s wants %s v%d but %s wants v%d — the packs disagree; their authors align them, or the project keeps its current selection", w.from, g, w.version, pk.Pack, v))
				}
				continue
			}
			wanted[g] = want{v, pk.Pack}
			order = append(order, g)
		}
		for _, p := range ps.Policies {
			carriedPolicies[p.Name] = true
		}
		for _, w := range ps.Workflows {
			lanes[w] = true
			if v, ok := pins.Workflows[w]; ok {
				if had, seen := laneVersion[w]; seen && had != v {
					c.Problems = append(c.Problems, fmt.Sprintf("two packs pin %s at different versions (v%d and v%d) — their authors align them", w, had, v))
				}
				laneVersion[w] = v
			}
		}
	}
	// The project's own blocks: kept at the version it has, else latest.
	have := map[string]ReceiptBlock{}
	for _, b := range cur.Receipt.Guidance {
		have[b.Name] = b
	}
	for ai, entry := range c.Additions {
		// An addition may name the version it wants ("block@6"): the way to
		// move a project's own block to a newer version, since a held
		// addition otherwise keeps the version it adopted, and a newer
		// version's checks would reach no project holding it. The candidate
		// keeps the bare name; the version lives in the receipt.
		name, askedVersion := entry, 0
		if i := strings.LastIndex(entry, "@"); i > 0 {
			name = entry[:i]
			fmt.Sscanf(entry[i+1:], "%d", &askedVersion)
			c.Additions[ai] = name
		}
		if _, fromPack := wanted[name]; fromPack {
			continue // a pack carries it now: not an addition any more
		}
		if askedVersion > 0 {
			b, err := a.blockByName(ctx, name)
			if err != nil {
				c.Problems = append(c.Problems, fmt.Sprintf("%s has no version %d", name, askedVersion))
				continue
			}
			if ok, err := a.hasBlockVersion(ctx, b.ID, askedVersion); err != nil || !ok {
				c.Problems = append(c.Problems, fmt.Sprintf("%s has no version %d", name, askedVersion))
				continue
			}
			wanted[name] = want{askedVersion, "own"}
		} else if b, ok := have[name]; ok {
			wanted[name] = want{b.Version, "own"}
		} else {
			b, err := a.blockByName(ctx, name)
			if err != nil {
				c.Problems = append(c.Problems, fmt.Sprintf("no block named %q", name))
				continue
			}
			wanted[name] = want{b.LatestVersion, "own"}
		}
		order = append(order, name)
	}
	// The receipt.
	var blocks []ReceiptBlock
	for i, name := range order {
		w := wanted[name]
		blk, err := a.blockByName(ctx, name)
		if err != nil {
			c.Problems = append(c.Problems, fmt.Sprintf("no block named %q", name))
			continue
		}
		exists, err := a.hasBlockVersion(ctx, blk.ID, w.version)
		if err != nil {
			return nil, err
		}
		if !exists {
			c.Problems = append(c.Problems, fmt.Sprintf("%s has no version %d", name, w.version))
			continue
		}
		b := ReceiptBlock{Position: i, Name: name, BlockID: blk.ID, Version: w.version, Tier: blk.Tier}
		if old, ok := have[name]; ok && old.Disabled && w.from == "own" {
			b.Disabled = true
		}
		blocks = append(blocks, b)
	}
	conflicts, err := a.declaredConflicts(ctx, blocks)
	if err != nil {
		return nil, err
	}
	for _, s := range conflicts {
		c.Problems = append(c.Problems, s+" — both would be in force")
	}
	// Lanes: the packs' plus every lane selected now that no pack of the
	// candidate's carries (a person's own selection stays).
	receipt := cur.Receipt
	receipt.Guidance = blocks
	receipt.Pack = ""
	if len(c.Packs) > 0 {
		receipt.Pack = c.Packs[0].Pack
		for _, pk := range c.Packs {
			if pk.Pack != PlatformPack {
				receipt.Pack = pk.Pack
				break
			}
		}
	}
	var wfs []ReceiptWorkflow
	keep := map[string]bool{}
	for _, wf := range cur.Receipt.Workflows {
		keep[wf.Name] = true
	}
	entries, err := a.ways.Workflows(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Doc.Metadata.System && !keep[e.Name] && !lanes[e.Name] {
			continue
		}
		// A pack's pin wins; a lane no pack pins keeps the version the
		// project is on; a lane new to the project comes at the library's latest.
		version, yamlSrc := e.Version, e.YAML
		if v, ok := laneVersion[e.Name]; ok {
			y, err := a.workflowYAML(ctx, e.Name, v)
			if err != nil {
				return nil, fmt.Errorf("pack workflow %s v%d is unavailable: %w", e.Name, v, err)
			}
			version, yamlSrc = v, y
		} else {
			for _, wf := range cur.Receipt.Workflows {
				if wf.Name == e.Name && wf.Version != e.Version {
					y, err := a.workflowYAML(ctx, e.Name, wf.Version)
					if err != nil {
						return nil, fmt.Errorf("adopted workflow %s v%d is unavailable: %w", e.Name, wf.Version, err)
					}
					version, yamlSrc = wf.Version, y
				}
			}
		}
		origin := e.OriginRef()
		if at := strings.LastIndex(origin, "@"); at > 0 {
			origin = fmt.Sprintf("%s@%d", origin[:at], version)
		}
		wfs = append(wfs, ReceiptWorkflow{Name: e.Name, Version: version, Origin: origin, YAMLHash: HashText(yamlSrc), System: e.Doc.Metadata.System})
	}
	receipt.Workflows = wfs
	c.Receipt = receipt
	c.Kept = keptFromPacks(view.Packs, lanes, carriedPolicies, receipt)
	c.Digest = Digest(receipt)
	// The diff, in words.
	now := map[string]ReceiptBlock{}
	for _, b := range blocks {
		now[b.Name] = b
	}
	for _, b := range blocks {
		old, had := have[b.Name]
		switch {
		case !had || (old.Disabled && !b.Disabled):
			c.Added = append(c.Added, fmt.Sprintf("%s@%d", b.Name, b.Version))
		case old.Version != b.Version:
			c.Changed = append(c.Changed, fmt.Sprintf("%s v%d→v%d", b.Name, old.Version, b.Version))
		}
	}
	c.NewChecks, err = a.newChecks(ctx, cur.Receipt, c.Receipt)
	if err != nil {
		return nil, err
	}
	for _, old := range cur.Receipt.Guidance {
		b, still := now[old.Name]
		if (!still || b.Disabled) && !old.Disabled {
			c.Removed = append(c.Removed, fmt.Sprintf("%s@%d", old.Name, old.Version))
		}
	}
	c.Weakens, err = a.weakenings(ctx, cur, view.Carried, c.Receipt)
	if err != nil {
		return nil, err
	}
	oldLanes := map[string]int{}
	for _, wf := range cur.Receipt.Workflows {
		oldLanes[wf.Name] = wf.Version
	}
	for _, wf := range wfs {
		if v, had := oldLanes[wf.Name]; !had {
			c.LanesAdded = append(c.LanesAdded, wf.Name)
		} else if v != wf.Version {
			c.Changed = append(c.Changed, fmt.Sprintf("workflow %s v%d→v%d", wf.Name, v, wf.Version))
		}
	}
	for _, wf := range cur.Receipt.Workflows {
		found := false
		for _, n := range wfs {
			if n.Name == wf.Name {
				found = true
			}
		}
		if !found {
			c.LanesRemoved = append(c.LanesRemoved, wf.Name)
		}
	}
	sort.Strings(c.Weakens)
	return c, nil
}

func gating(sev string) bool { return sev == "high" || sev == "critical" }

// declaredConflicts among blocks in force.
func (a candidateBuilder) declaredConflicts(ctx context.Context, blocks []ReceiptBlock) ([]string, error) {
	names := map[string]bool{}
	for _, b := range blocks {
		if !b.Disabled {
			names[b.Name] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range blocks {
		if b.Disabled {
			continue
		}
		var conflicts string
		blk, err := a.blockByID(ctx, b.BlockID)
		if err != nil {
			return nil, err
		}
		conflicts = blk.Conflicts
		for _, c := range strings.Split(conflicts, ",") {
			if c = strings.TrimSpace(c); c != "" && names[c] {
				pair := []string{b.Name, c}
				sort.Strings(pair)
				k := strings.Join(pair, "|")
				if !seen[k] {
					seen[k] = true
					out = append(out, fmt.Sprintf("%s contradicts %s", pair[0], pair[1]))
				}
			}
		}
	}
	return out, nil
}

type candidatePackSpec struct {
	Platform  bool     `json:"platform"`
	Guidance  []string `json:"guidance"`
	Workflows []string `json:"workflows"`
	Policies  []struct {
		Name string `json:"name"`
	} `json:"policies"`
	Requires []string `json:"requires,omitempty"`
}

// keptFromPacks is what the project's current packs supplied that the
// candidate's packs do not carry and the candidate keeps: a selected
// workflow, or a policy installed from a pack.
func keptFromPacks(current []ProjectPackRecord, lanes, policies map[string]bool, receipt Receipt) []string {
	supplied := map[string]bool{}
	for _, p := range current {
		var spec candidatePackSpec
		if json.Unmarshal([]byte(p.SpecJSON), &spec) != nil {
			continue
		}
		for _, w := range spec.Workflows {
			supplied[w] = true
		}
	}
	out := []string{}
	for _, wf := range receipt.Workflows {
		if supplied[wf.Name] && !lanes[wf.Name] && !wf.System {
			out = append(out, "workflow "+wf.Name)
		}
	}
	for _, p := range receipt.Policies {
		if strings.HasPrefix(p.Origin, "pack/") && !policies[p.Name] {
			out = append(out, "policy "+p.Name)
		}
	}
	return out
}

type candidatePublication struct {
	Spec candidatePackSpec
	Pins wow.PinSet
}

func parseCandidatePublication(name string, version int64, p wow.PackVersion) (candidatePublication, error) {
	var out candidatePublication
	if err := json.Unmarshal([]byte(p.SpecJSON), &out.Spec); err != nil {
		return out, fmt.Errorf("%s v%d: invalid spec: %w", name, version, err)
	}
	err := json.Unmarshal([]byte(p.PinsJSON), &out.Pins)
	return out, err
}

func (a candidateBuilder) packPublication(ctx context.Context, name string, version int64) (candidatePublication, error) {
	p, err := a.ways.PackPublication(ctx, name, version)
	if err != nil {
		return candidatePublication{}, err
	}
	return parseCandidatePublication(name, version, p)
}

// Adoption rereads immutable declarations at settlement so a caller cannot
// remove a published requirement by editing a prepared candidate's summary.
func requiredCapabilitiesIn(ctx context.Context, q database.Queryer, c Candidate) ([]string, error) {
	out := slices.Clone(c.RequiredCapabilities)
	for _, p := range c.Packs {
		publication, err := wow.PackVersionIn(ctx, q, p.Pack, p.Version)
		if err != nil {
			return nil, err
		}
		parsed, err := parseCandidatePublication(p.Pack, p.Version, publication)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed.Spec.Requires...)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

type candidateView struct {
	Packs     []ProjectPackRecord
	Additions []ReceiptBlock
	Carried   map[string]bool
}

func candidatePlacement(current *RecordedRevision, packs []ProjectPackRecord) (candidateView, error) {
	out := candidateView{Packs: packs, Additions: []ReceiptBlock{}, Carried: map[string]bool{}}
	for _, p := range packs {
		var spec candidatePackSpec
		if err := json.Unmarshal([]byte(p.SpecJSON), &spec); err != nil {
			return out, err
		}
		for _, name := range spec.Guidance {
			out.Carried[name] = true
		}
	}
	for _, b := range current.Receipt.Guidance {
		if !b.Disabled && !out.Carried[b.Name] {
			out.Additions = append(out.Additions, b)
		}
	}
	return out, nil
}

func (a candidateBuilder) blockByName(ctx context.Context, name string) (*wow.Block, error) {
	b, err := a.ways.Block(ctx, wow.BlockRef{Name: name})
	return &b.Block, err
}
func (a candidateBuilder) blockByID(ctx context.Context, id ids.ID) (*wow.Block, error) {
	b, err := a.ways.Block(ctx, wow.BlockRef{ID: id})
	return &b.Block, err
}
func (a candidateBuilder) hasBlockVersion(ctx context.Context, id ids.ID, version int) (bool, error) {
	_, err := a.ways.Block(ctx, wow.BlockRef{ID: id, Version: version})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (a candidateBuilder) workflowYAML(ctx context.Context, name string, version int) (string, error) {
	w, err := a.ways.Workflow(ctx, wow.WorkflowRef{Name: name, Version: version})
	return w.YAML, err
}

type candidateCheck struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Expects   string `json:"expects"`
	Severity  string `json:"severity"`
	Evaluator string `json:"evaluator"`
	Probe     string `json:"probe"`
}

func (a candidateBuilder) checksOfBlock(ctx context.Context, id ids.ID, version int) ([]candidateCheck, error) {
	b, err := a.ways.Block(ctx, wow.BlockRef{ID: id, Version: version})
	if err != nil {
		return nil, err
	}
	var checks []candidateCheck
	if err := json.Unmarshal([]byte(b.ChecksJSON), &checks); err != nil {
		return nil, err
	}
	return checks, nil
}

// weakeningsIn reobserves the selected manifest under the caller's transaction.
// A caller's presentation summary is never an authority input.
func (s *Service) weakeningsIn(ctx context.Context, q database.Queryer, projectID ids.ID, c Candidate) ([]string, error) {
	current, err := CurrentIn(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if current.Revision == 0 {
		return nil, nil
	}
	prior, err := CurrentReceiptIn(ctx, q, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project %d: revision %d: %w", projectID, current.Revision, ErrRevisionUnavailable)
	}
	if err != nil {
		return nil, err
	}
	memberships, err := ProjectPacksIn(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	placement, err := candidatePlacement(prior, memberships)
	if err != nil {
		return nil, err
	}
	return (candidateBuilder{ways: s.deps.Ways}).weakenings(ctx, prior, placement.Carried, c.Receipt)
}

func (a candidateBuilder) weakenings(ctx context.Context, prior *RecordedRevision, carried map[string]bool, receipt Receipt) ([]string, error) {
	now := make(map[string]ReceiptBlock, len(receipt.Guidance))
	for _, b := range receipt.Guidance {
		now[b.Name] = b
	}
	out := []string{}
	for _, old := range prior.Receipt.Guidance {
		b, still := now[old.Name]
		if old.Disabled || !carried[old.Name] || (still && !b.Disabled) {
			continue
		}
		checks, err := a.checksOfBlock(ctx, old.BlockID, old.Version)
		if err != nil {
			return nil, err
		}
		for _, check := range checks {
			if gating(check.Severity) {
				out = append(out, old.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Words says what the candidate brings, for a Change's title and body:
// "go-small v2, AppTown v3, 1 block(s) of its own".
func (c *Candidate) Words() []string {
	var out []string
	for _, pk := range c.Packs {
		out = append(out, fmt.Sprintf("%s v%d", pk.Pack, pk.Version))
	}
	if len(c.Additions) > 0 {
		out = append(out, fmt.Sprintf("%d block(s) of its own", len(c.Additions)))
	}
	return out
}
