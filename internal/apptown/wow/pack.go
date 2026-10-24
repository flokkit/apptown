package wow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	yamlv3 "gopkg.in/yaml.v3"
)

// A pack is a named way of working: the guidance, workflows and policies a
// project takes together (state-model §6.4). Stock packs ship in
// stock/packs.yaml and re-sync with the release; custom packs are the
// instance's own. The stored row keeps the spec as JSON (PackRecord); Pack
// is the decoded shape every reader and the wire use.

// PackPolicy is a library policy a pack names. A pack declares only the
// name; a published version also holds the definition it was published
// with (description, rule, options), so the version pins it.
type PackPolicy struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	Rule        string         `yaml:"rule" json:"rule"`
	With        map[string]any `yaml:"with" json:"with"`
}

// UnmarshalYAML reads a pack policy as written in packs.yaml: a library
// policy's name, nothing else.
func (pp *PackPolicy) UnmarshalYAML(node *yamlv3.Node) error {
	if node.Kind != yamlv3.ScalarNode {
		return fmt.Errorf("line %d: a pack names a library policy; define the policy in policies.yaml", node.Line)
	}
	return node.Decode(&pp.Name)
}

type Pack struct {
	// APIVersion optionally states the format (schema.APIVersion); absent
	// is the same. It is checked when the pack is read or saved and never
	// stored: stating it does not make a different publication.
	APIVersion    string                    `yaml:"apiVersion,omitempty" json:"apiVersion,omitempty"`
	Resources     appcontract.MemoryDefault `yaml:"resources,omitempty" json:"resources,omitzero"`
	ID            ids.ID                    `yaml:"-" json:"-"`
	LatestVersion int64                     `yaml:"-" json:"-"`

	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	// Platform: AppTown's own tier (design/subsystems/ways-of-working-versions.md
	// §1.1) — every repo-backed project is on it; never offered as a choice.
	Platform  bool         `yaml:"platform" json:"platform,omitempty"`
	Guidance  []string     `yaml:"guidance" json:"guidance"`
	Workflows []string     `yaml:"workflows" json:"workflows"`
	Policies  []PackPolicy `yaml:"policies" json:"policies"`
	// Toolchain names a stock/toolchains/<name>.toml written as the
	// project's mise.toml at pack apply — a pack whose guidance says "the
	// toolchain is declared in mise.toml" must deliver that declaration.
	Toolchain string `yaml:"toolchain" json:"toolchain,omitempty"`
	// Tools names the version line of each tool the pack prescribes
	// (node: "24", postgres: "16"): a line, never an exact version, which
	// the tool versions list resolves. Changing a line is a new version of
	// the pack, adopted like any other.
	Tools map[string]string `yaml:"tools" json:"tools,omitempty"`
	// Services are the sidecars this pack PROPOSES (a database, a cache) —
	// pre-selected in the creation wizard, freely tuned. A server pack that
	// says "Postgres" declares it here; a client-only pack declares none.
	Services []deploy.ServiceSpec `yaml:"services" json:"services,omitempty"`
	// Extensions are opt-in pi extensions this pack turns on at apply —
	// e.g. a TS/Python/Go pack enables "lsp".
	Extensions []string `yaml:"extensions" json:"extensions,omitempty"`
	// Requires names instance capabilities a project on this pack needs
	// (a scanner): adoption refuses the pack until each is attached.
	Requires []string `yaml:"requires" json:"requires,omitempty"`
	// Includes, Replaces, Drops and Clears are the inclusion declaration
	// (design/subsystems/ways-of-working-versions.md): this pack is another pack
	// ("name" follows its offered version, "name@4" pins one), with these
	// items swapped or removed and this pack's own items added. A version
	// stores the resolved result; a pack read for application is resolved.
	Includes string            `yaml:"includes" json:"includes,omitempty"`
	Replaces map[string]string `yaml:"replaces" json:"replaces,omitempty"`
	Drops    []PackDrop        `yaml:"drops" json:"drops,omitempty"`
	Clears   []string          `yaml:"clears" json:"clears,omitempty"`
	// Starter is the pack whose starter files (stock/seeds/<pack>) a
	// project made from this one is seeded with, when it is not this pack:
	// an including pack starts like the pack it includes. Resolution sets it.
	Starter string `yaml:"-" json:"starter,omitempty"`
	// Version is the publication these contents were read from: the one
	// offered to new projects. 0 when the pack has none.
	Version int64  `yaml:"-" json:"version,omitempty"`
	Origin  string `yaml:"-" json:"origin"` // "stock" = vendor (read-only, re-synced); "" = custom
	// Enabled is the instance's switch: an off pack stays in the store
	// (inspectable, clonable, re-enablable) but is not offered when a
	// project is made — by the picker, the interview, or the wizard.
	// Stock packs cannot be deleted (they re-sync); they can be turned off.
	Enabled bool `yaml:"-" json:"enabled"`
}

// PackDrop removes an included item, saying why.
type PackDrop struct {
	Item   string `yaml:"item" json:"item"`
	Reason string `yaml:"reason" json:"reason"`
}

// PackSpec is the stored shape (name and origin live in columns): the
// declaration on the pack row, the resolved contents on a version.
type PackSpec struct {
	Resources appcontract.MemoryDefault `json:"resources,omitzero"`
	Platform  bool                      `json:"platform,omitempty"`
	Includes  string                    `json:"includes,omitempty"`
	Replaces  map[string]string         `json:"replaces,omitempty"`
	Drops     []PackDrop                `json:"drops,omitempty"`
	// Clears names inherited setup fields (toolchain, tools, services) this pack
	// sets to nothing; an omitted field is inherited.
	Clears     []string             `json:"clears,omitempty"`
	Starter    string               `json:"starter,omitempty"`
	Guidance   []string             `json:"guidance"`
	Workflows  []string             `json:"workflows"`
	Policies   []PackPolicy         `json:"policies"`
	Toolchain  string               `json:"toolchain,omitempty"`
	Tools      map[string]string    `json:"tools,omitempty"`
	Services   []deploy.ServiceSpec `json:"services,omitempty"`
	Extensions []string             `json:"extensions,omitempty"`
	Requires   []string             `json:"requires,omitempty"`
}

// Spec is what the store keeps of the pack.
func (p Pack) Spec() PackSpec {
	return PackSpec{Resources: p.Resources, Platform: p.Platform, Includes: p.Includes, Replaces: p.Replaces, Drops: p.Drops, Clears: p.Clears,
		Guidance: p.Guidance, Workflows: p.Workflows, Policies: p.Policies,
		Toolchain: p.Toolchain, Tools: p.Tools, Services: p.Services, Extensions: p.Extensions, Requires: p.Requires}
}

// Pack decodes the row's declaration into the shape readers use. A pack
// that includes another is only its own items here; Service.Pack reads the
// resolved contents.
func (p PackRecord) Pack() (Pack, error) {
	return packFrom(p, p.SpecJSON)
}

// packFrom decodes a stored spec. One that does not decode is an error
// naming the pack: a corrupt record is not a pack with no guidance.
func packFrom(p PackRecord, spec string) (Pack, error) {
	pk := Pack{ID: p.ID, LatestVersion: p.LatestVersion, Name: p.Name, Description: p.Description, Origin: p.Origin, Enabled: p.Enabled}
	var ps PackSpec
	if err := json.Unmarshal([]byte(spec), &ps); err != nil {
		return pk, fmt.Errorf("pack %s: its stored spec cannot be read: %w", p.Name, err)
	}
	pk.Guidance, pk.Workflows, pk.Policies, pk.Toolchain, pk.Services, pk.Extensions, pk.Platform = ps.Guidance, ps.Workflows, ps.Policies, ps.Toolchain, ps.Services, ps.Extensions, ps.Platform
	pk.Resources, pk.Tools = ps.Resources, ps.Tools
	pk.Includes, pk.Replaces, pk.Drops, pk.Clears, pk.Starter, pk.Requires = ps.Includes, ps.Replaces, ps.Drops, ps.Clears, ps.Starter, ps.Requires
	return pk, nil
}

// StarterPack names the pack whose starter files seed a new project.
func (p Pack) StarterPack() string {
	if p.Starter != "" {
		return p.Starter
	}
	return p.Name
}

// ReadStockPacks reads the packs the release ships (packs.yaml).
func ReadStockPacks(fsys fs.FS) ([]Pack, error) {
	raw, err := fs.ReadFile(fsys, "packs.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packs []Pack `yaml:"packs"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return nil, fmt.Errorf("packs.yaml: %w", err)
	}
	for _, p := range doc.Packs {
		if err := schema.CheckAPIVersion(p.APIVersion); err != nil {
			return nil, fmt.Errorf("packs.yaml: pack %s: %w", p.Name, err)
		}
	}
	return doc.Packs, nil
}

// ReadStockPolicies reads the policies the release ships (policies.yaml),
// each held to its rule's options.
func ReadStockPolicies(fsys fs.FS) ([]LibraryPolicy, error) {
	raw, err := fs.ReadFile(fsys, "policies.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Policies []LibraryPolicy `yaml:"policies"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return nil, fmt.Errorf("policies.yaml: %w", err)
	}
	for _, p := range doc.Policies {
		if _, err := CheckPolicyAuthored(p.Name, p.Rule, p.With); err != nil {
			return nil, fmt.Errorf("policies.yaml: %w", err)
		}
	}
	return doc.Policies, nil
}

// decodeStrict reads one YAML document into v, refusing a key v does not
// have: a misspelt field is an error, never silently nothing.
func decodeStrict(raw []byte, v any) error {
	dec := yamlv3.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ErrUnknownToolLine is a pack naming a tool line the tool versions list
// does not have, or proposing a service whose image disagrees with its line.
var ErrUnknownToolLine = fault.New(fault.Invalid, "a pack names only tool lines the tool versions list has")

// checkTools holds a pack's resolved tools to the tool versions list, and a
// proposed service's image tag to the pack's line for that tool: the tag is
// the line, or the line and a dot and more (postgres:16, mariadb:11.4.13 on
// line 11.4).
func (s *Packs) checkTools(spec PackSpec) error {
	for _, tool := range slices.Sorted(maps.Keys(spec.Tools)) {
		line := spec.Tools[tool]
		if s.lines == nil || !s.lines.HasLine(tool, line) {
			return fmt.Errorf("%w: %s %q is not in the tool versions this AppTown has", ErrUnknownToolLine, tool, line)
		}
	}
	for _, svc := range spec.Services {
		line, ok := spec.Tools[svc.Name]
		if !ok || svc.Image == "" {
			continue
		}
		_, tag, found := strings.Cut(svc.Image[strings.LastIndex(svc.Image, "/")+1:], ":")
		tag, _, _ = strings.Cut(tag, "@")
		if !found || (tag != line && !strings.HasPrefix(tag, line+".")) {
			return fmt.Errorf("%w: service %s proposes %s, which is not %s line %s", ErrUnknownToolLine, svc.Name, svc.Image, svc.Name, line)
		}
	}
	return nil
}
