package wow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/strs"
)

// LibraryPolicy is a named definition at the instance level: stock entries
// re-sync with the release, custom ones are the institution's.
type LibraryPolicy struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	Rule        string         `yaml:"rule" json:"rule"`
	With        map[string]any `yaml:"with" json:"with"`
	Origin      string         `yaml:"-" json:"origin"`
}

var (
	ErrPolicyDeclaration = fault.New(fault.Invalid, "invalid policy declaration")
	ErrStockPolicy       = fault.New(fault.Conflict, "a stock policy re-syncs with the release; clone it under a new name")
	// ErrNoLibraryPolicy is the absence of the library policy a caller named.
	ErrNoLibraryPolicy = fault.New(fault.NotFound, "no library policy is named")
)

func invalidPolicy(name string, format string, args ...any) error {
	return fmt.Errorf("%w: policy %q: %s", ErrPolicyDeclaration, name, fmt.Sprintf(format, args...))
}

// Requirement is the checked rule every evaluator uses. The persisted
// declaration may carry extra fields; decisions use these named options.
type Requirement struct {
	rule       string
	Uses       string `json:"uses"`
	Requires   string `json:"requires"`
	Capability string `json:"capability"`
	Max        string `json:"max"`
}

// ruleOptions is the options each rule takes.
var ruleOptions = map[string][]string{
	"requireStepBefore":      {"uses", "requires"},
	"requireCapabilityBound": {"capability"},
	"scanFailOnCeiling":      {"max"},
}

// CheckPolicyAuthored is CheckPolicy for a definition being written now (a
// library save, a project's own policy, the release's policies.yaml): an
// option the rule does not take is a mistake, not a no-op. A stored
// definition, accepted under the rules of its day, is read with
// CheckPolicy.
func CheckPolicyAuthored(name, rule string, with map[string]any) (Requirement, error) {
	requirement, err := CheckPolicy(name, rule, with)
	if err != nil {
		return requirement, err
	}
	for key := range with {
		if !slices.Contains(ruleOptions[rule], key) {
			return requirement, invalidPolicy(name, "rule %s takes %s, not %q", rule, strings.Join(ruleOptions[rule], " and "), key)
		}
	}
	return requirement, nil
}

// CheckPolicy is the declaration rule: a known rule with the options it
// needs, and nothing malformed.
func CheckPolicy(name, rule string, with map[string]any) (Requirement, error) {
	raw, err := json.Marshal(with)
	if err != nil {
		return Requirement{}, invalidPolicy(name, "%v", err)
	}
	requirement := Requirement{rule: rule}
	if err := json.Unmarshal(raw, &requirement); err != nil {
		return requirement, invalidPolicy(name, "invalid options: %v", err)
	}

	switch rule {
	case "requireStepBefore":
		if requirement.Uses == "" || requirement.Requires == "" || strings.TrimSpace(requirement.Uses) != requirement.Uses || strings.TrimSpace(requirement.Requires) != requirement.Requires {
			return requirement, invalidPolicy(name, "uses and requires step types are required")
		}
	case "requireCapabilityBound":
		if requirement.Capability == "" || strings.TrimSpace(requirement.Capability) != requirement.Capability {
			return requirement, invalidPolicy(name, "a capability is required")
		}
	case "scanFailOnCeiling":
		switch requirement.Max {
		case "", "low", "medium", "high", "critical":
		default:
			return requirement, invalidPolicy(name, "invalid scan ceiling %q", requirement.Max)
		}
	default:
		return requirement, invalidPolicy(name, "unknown rule %q", rule)
	}
	return requirement, nil
}

// Evaluate judges one workflow definition against the rule; "" is conformant.
func (p Requirement) Evaluate(spec *schema.Spec, bound bool) string {
	switch p.rule {
	case "requireStepBefore":
		firstUses, firstReq := -1, -1
		for i, stage := range spec.Stages {
			for _, steps := range [][]schema.Step{stage.OnEnter, stage.Steps} {
				for _, step := range steps {
					if step.Uses == p.Uses && firstUses == -1 {
						firstUses = i
					}
					if step.Uses == p.Requires && firstReq == -1 {
						firstReq = i
					}
				}
			}
		}
		if firstUses >= 0 && (firstReq < 0 || firstReq >= firstUses) {
			return fmt.Sprintf("has a %q step (stage %q) without a %q step in an earlier stage", p.Uses, spec.Stages[firstUses].Name, p.Requires)
		}
	case "requireCapabilityBound":
		if !bound {
			return fmt.Sprintf("instance has no binding for capability %q", p.Capability)
		}
	case "scanFailOnCeiling":
		for _, stage := range spec.Stages {
			for _, steps := range [][]schema.Step{stage.OnEnter, stage.Steps} {
				for _, step := range steps {
					if step.Uses != "scan" {
						continue
					}
					failOn, _ := step.With["failOn"].(string)
					if FailOnLooseness(failOn) > FailOnLooseness(p.Max) {
						return fmt.Sprintf("scan step %q sets failOn=%q, looser than the policy ceiling %q", step.Name, failOn, p.Max)
					}
				}
			}
		}
	}
	return ""
}

// FailOnLooseness orders failOn thresholds by how permissive they are: ""
// (any actionable finding fails) is strictest; critical is loosest.
func FailOnLooseness(s string) int {
	switch s {
	case "":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 4
}

// Reading renders the precise plain-words reading of a rule; the server
// owns the wording so the UI cannot drift.
func Reading(rule string, with map[string]any) string {
	switch rule {
	case "requireStepBefore":
		return fmt.Sprintf("any workflow containing a %q step must have a %q step in a strictly earlier stage; checked when workflows are saved (a save that breaks this is refused), warned on runs of workflows saved before the policy",
			optString(with, "uses"), optString(with, "requires"))
	case "requireCapabilityBound":
		return fmt.Sprintf("this AppTown must have a working %q integration; checked when workflows are saved", optString(with, "capability"))
	case "scanFailOnCeiling":
		return fmt.Sprintf("no workflow may tell a security scan to ignore findings worse than %q; checked when workflows are saved", optString(with, "max"))
	}
	return rule
}

// Fix says what to do about a violation: the answer to "why can't I
// release" always comes with a way forward.
func Fix(rule string, with map[string]any) string {
	switch rule {
	case "requireStepBefore":
		return fmt.Sprintf("add a %q step to a stage before the %q step (or ask the helper to)", optString(with, "requires"), optString(with, "uses"))
	case "requireCapabilityBound":
		return fmt.Sprintf("connect a %q integration on the Integrations page", optString(with, "capability"))
	case "scanFailOnCeiling":
		return fmt.Sprintf("set the scan's failOn back to %q or stricter", optString(with, "max"))
	}
	return ""
}

// Policies is the supported write API over a project's policies and the
// instance's policy library.
type Policies struct {
	db    *database.DB
	clock func() time.Time // the forge's clock; nil is the real one
}

func (s *Policies) now() time.Time { return clock.Or(s.clock)() }

func NewPolicies(db *database.DB) *Policies { return &Policies{db: db} }

// LibraryByName is one library definition, or nil when there is none.
func (s *Policies) LibraryByName(ctx context.Context, name string) *LibraryPolicy {
	r, err := wowsql.New(s.db.SQL()).LibraryPolicyByName(ctx, name)
	if err != nil {
		return nil
	}
	lp := libraryPolicyRecord(r)
	return &lp
}

// LibraryAll lists the library, stock first.
func (s *Policies) LibraryAll(ctx context.Context) ([]LibraryPolicy, error) {
	rows, err := wowsql.New(s.db.SQL()).LibraryPolicies(ctx)
	out := make([]LibraryPolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, libraryPolicyRecord(r))
	}
	return out, err
}

// SaveLibrary creates or updates a custom definition with its receipt;
// stock definitions are clone-only.
func (s *Policies) SaveLibrary(ctx context.Context, in LibraryPolicy, who string) error {
	now := s.now().UTC()
	if _, err := CheckPolicyAuthored(in.Name, in.Rule, in.With); err != nil {
		return err
	}
	config, err := strs.JSON(in.With)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		if r, err := q.LibraryPolicyByName(ctx, in.Name); err == nil && r.Origin == "stock" {
			return fmt.Errorf("%q: %w", in.Name, ErrStockPolicy)
		}
		n, err := q.EditLibraryPolicy(ctx, wowsql.EditLibraryPolicyParams{Name: in.Name, Description: in.Description, Rule: in.Rule, Config: []byte(config), At: now})
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.InsertLibraryPolicy(ctx, wowsql.InsertLibraryPolicyParams{ID: ids.New(now), Name: in.Name, Description: in.Description, Rule: in.Rule, Config: []byte(config), At: now}); err != nil {
				return err
			}
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "policy-library.saved", in.Name, map[string]any{"rule": in.Rule, "with": in.With})
	})
}

// DeleteLibrary removes a custom definition with its receipt; projects that
// adopted it keep their copies. Stock definitions are refused with
// ErrStockPolicy; a missing one is ErrNoLibraryPolicy.
func (s *Policies) DeleteLibrary(ctx context.Context, name, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.LibraryPolicyByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w %q", ErrNoLibraryPolicy, name)
		}
		if err != nil {
			return err
		}
		if r.Origin == "stock" {
			return fmt.Errorf("%q: %w", name, ErrStockPolicy)
		}
		if _, err := q.DeleteLibraryPolicy(ctx, name); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "policy-library.deleted", name, nil)
	})
}

func libraryPolicyRecord(r wowsql.PolicyLibrary) LibraryPolicy {
	lp := LibraryPolicy{Name: r.Name, Description: r.Description, Rule: r.Rule, Origin: r.Origin}
	_ = json.Unmarshal(r.ConfigJson, &lp.With)
	return lp
}

func optString(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}
