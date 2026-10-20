// Package schema parses and validates Forge YAML documents (Workflow, Policy).
// See design/architecture/workflow-spec.md.
package schema

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Doc struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Category shelves the library (closed set): change | landing |
	// maintenance | integration | knowledge. Display only — matching a
	// workflow to a project is packs' job (and someday declared
	// requirements), not a taxonomy's.
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
	// Deprecated: still runnable where installed, but the library labels
	// it, pack pulls skip it, and pickers gray it. Retirement is a pack
	// update; deprecation is the warning shot.
	Deprecated bool   `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
	Project    string `yaml:"project,omitempty" json:"project,omitempty"`
	// System: the apptown runs this itself (landing, shipping, wiring an
	// integration in or out, the scheduled patch). Never offered to a person
	// as a way of working; never needs selecting by a project
	// (design/decisions/workflows-as-lanes.md).
	System bool `yaml:"system,omitempty" json:"system,omitempty"`
	// Tier: how sure the forge is that a person needs this way of working.
	// "core" is the short list everyone sees first; "extra" is the longer
	// tail; system workflows are their own tier regardless. Unset = extra.
	Tier string `yaml:"tier,omitempty" json:"tier,omitempty"`
}

// EffectiveTier: "system" | "core" | "extra".
func (m Metadata) EffectiveTier() string {
	if m.System {
		return "system"
	}
	if m.Tier == "core" {
		return "core"
	}
	return "extra"
}

type Spec struct {
	// V is the shape version stamped into a run's pinned definition by the
	// engine (never written in YAML). 0 means "before versions existed".
	V    int    `yaml:"-" json:"v,omitempty"`
	Kind string `yaml:"kind" json:"kind"` // lifecycle | pipeline
	// Role in the Change lifecycle (design/subsystems/dev-loop.md): "working" runs
	// accrue commits to the Change's branch and never merge; "landing" runs
	// are the only path to merge+ship+release. An omitted role is
	// "working" — the old fused behaviour (work and land in one run) is
	// gone: research and product-spec ran through it and stopping them
	// deleted the work. A "record" run's outcome is
	// the records it keeps (a report, a specification), not commits:
	// accepting it closes the change and nothing lands, so a finished
	// research question does not merge an empty branch and redeploy the app.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	On   On     `yaml:"on,omitempty" json:"on,omitempty"`
	// Workspace: "run" (default — fresh dir per run) or "project" (one
	// persistent working tree per project; runs mutate it in place — assumes
	// serialized runs; precursor to git-backed project state).
	Workspace string   `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Defaults  Defaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Stages    []Stage  `yaml:"stages" json:"stages"`
}

// On declares a workflow's triggers. `manual` is always allowed regardless.
type On struct {
	Manual   map[string]any   `yaml:"manual,omitempty" json:"manual,omitempty"`
	Schedule *ScheduleTrigger `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// Webhook presence enables POST /api/v1/hooks/{project}/{workflow}
	// (authenticated by FORGE_WEBHOOK_TOKEN from secrets.env).
	Webhook map[string]any `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	// WorkItem is informational in v0 (work items already start runs).
	WorkItem map[string]any `yaml:"workItem,omitempty" json:"workItem,omitempty"`
}

type ScheduleTrigger struct {
	Cron string `yaml:"cron" json:"cron"` // standard 5-field cron
	// SkipIfActive (default true): don't fire while a run of this workflow is
	// still running in the project — prevents pileup of long-lived runs.
	SkipIfActive *bool `yaml:"skipIfActive,omitempty" json:"skipIfActive,omitempty"`
}

type Defaults struct {
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`       // agent/pi: "provider/model"
	Thinking string `yaml:"thinking,omitempty" json:"thinking,omitempty"` // agent/pi thinking level
	Executor string `yaml:"executor,omitempty" json:"executor,omitempty"` // local | docker
	Image    string `yaml:"image,omitempty" json:"image,omitempty"`       // docker executor image
}

type Stage struct {
	Name    string `yaml:"name" json:"name"`
	OnEnter []Step `yaml:"onEnter,omitempty" json:"onEnter,omitempty"`
	Steps   []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
	Gate    *Gate  `yaml:"gate,omitempty" json:"gate,omitempty"`
}

type Step struct {
	Name string         `yaml:"name" json:"name"`
	Uses string         `yaml:"uses" json:"uses"`
	With map[string]any `yaml:"with,omitempty" json:"with,omitempty"`
}

type Gate struct {
	Require []string `yaml:"require,omitempty" json:"require,omitempty"`
}

// SupportedSteps is the v0 step-type set. The full set from the spec
// (policy, agent/pi, scm/*, deploy, workflow) lands in later milestones.
var SupportedSteps = map[string]bool{
	"script":               true,
	"approval":             true,
	"artifact/require":     true,
	"adr/require":          true,
	"agent/pi":             true,
	"scm/mr":               true,
	"preview":              true,
	"deploy":               true,
	"workflow/call":        true, // composition.md §2: call a pipeline workflow as a step
	"scan":                 true, // integrations.md §5: tool-provider check against the workspace
	"agent/review":         true, // the cop model: independent claim-vs-diff verification
	"guidance/conformance": true,
	"tests":                true, // run the committed tree's tests; evidence is the receipt (teststep.go) // guidance-checks.md: imported blocks' checks vs the workspace
	"reconcile":            true, // land preflight: bring main into the change branch before gating (reconcile.go)
}

// ParseWorkflow validates a NEW or edited document — the save-time
// constitution. What it accepts defines what a workflow can BE.
func ParseWorkflow(src []byte) (*Doc, error) { return parseWorkflow(src, true) }

// ParseWorkflowStored reads an already-accepted document (the engine,
// summaries, triggers). New requirements apply prospectively: when the
// law tightens, history must stay readable and runnable — tightening is
// for the next save, not a hostage-taking of the record.
func ParseWorkflowStored(src []byte) (*Doc, error) { return parseWorkflow(src, false) }

// APIVersion is the one workflow format version this forge reads; a
// document without one is this version.
const APIVersion = "apptown.dev/v1"

// legacyChangeAlias is ${{workItem.*}}, the older name of ${{change.*}}.
// Stored versions that use it stay runnable; a save may not use it.
var legacyChangeAlias = regexp.MustCompile(`\$\{\{\s*workItem\.`)

// written is a document as saved: the workflow, and any top-level keys
// beyond it, which may only be x- keys (as in a compose file) holding
// anchors the document reuses.
type written struct {
	Doc   `yaml:",inline"`
	Extra map[string]yaml.Node `yaml:",inline"`
}

func parseWorkflow(src []byte, strict bool) (*Doc, error) {
	var w written
	dec := yaml.NewDecoder(bytes.NewReader(src))
	// At save a misspelt key is an error, not a field silently not set; a
	// stored definition was accepted under the rules of its day.
	dec.KnownFields(strict)
	if err := dec.Decode(&w); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid yaml: %w", err)
	}
	d := w.Doc
	if strict {
		for key := range w.Extra {
			if !strings.HasPrefix(key, "x-") {
				return nil, fmt.Errorf("unknown top-level key %q (only apiVersion, kind, metadata, spec, and x- keys for anchors)", key)
			}
		}
		if d.APIVersion != "" && d.APIVersion != APIVersion {
			return nil, fmt.Errorf("apiVersion %q is not one this AppTown reads (%s)", d.APIVersion, APIVersion)
		}
		if legacyChangeAlias.Match(src) {
			return nil, fmt.Errorf("${{workItem.*}} is the old name of ${{change.*}}; write change")
		}
	}
	if err := d.validate(strict); err != nil {
		return nil, err
	}
	return &d, nil
}

// Validate applies the full save-time rules.
func (d *Doc) Validate() error { return d.validate(true) }

func (d *Doc) validate(strict bool) error {
	if d.Kind != "Workflow" {
		return fmt.Errorf("kind must be \"Workflow\", got %q", d.Kind)
	}
	if d.Spec.Role == "" {
		d.Spec.Role = "working"
	}
	if d.Spec.Role != "working" && d.Spec.Role != "landing" && d.Spec.Role != "record" {
		return fmt.Errorf("spec.role must be working, landing or record, got %q", d.Spec.Role)
	}
	if d.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if c := d.Metadata.Category; c != "" {
		switch c {
		case "change", "landing", "maintenance", "integration", "knowledge":
		default:
			return fmt.Errorf("metadata.category must be one of change, landing, maintenance, integration, knowledge (got %q)", c)
		}
	}
	if d.Spec.Kind != "lifecycle" && d.Spec.Kind != "pipeline" {
		return fmt.Errorf("spec.kind must be \"lifecycle\" or \"pipeline\", got %q", d.Spec.Kind)
	}
	if d.Spec.Workspace != "" && d.Spec.Workspace != "run" && d.Spec.Workspace != "project" {
		return fmt.Errorf("spec.workspace must be \"run\" or \"project\", got %q", d.Spec.Workspace)
	}
	if len(d.Spec.Stages) == 0 {
		return fmt.Errorf("spec.stages: at least one stage is required")
	}
	seenStage := map[string]bool{}
	for i, st := range d.Spec.Stages {
		if st.Name == "" {
			return fmt.Errorf("stages[%d]: name is required", i)
		}
		if seenStage[st.Name] {
			return fmt.Errorf("duplicate stage name %q", st.Name)
		}
		seenStage[st.Name] = true

		all := append(append([]Step{}, st.OnEnter...), st.Steps...)
		if len(all) == 0 {
			return fmt.Errorf("stage %q: at least one step is required", st.Name)
		}
		seenStep := map[string]bool{}
		for _, s := range all {
			if s.Name == "" {
				return fmt.Errorf("stage %q: every step needs a name", st.Name)
			}
			if seenStep[s.Name] {
				return fmt.Errorf("stage %q: duplicate step name %q", st.Name, s.Name)
			}
			seenStep[s.Name] = true
			if !SupportedSteps[s.Uses] {
				return fmt.Errorf("stage %q step %q: unsupported uses %q (supported: script, approval, artifact/require, adr/require, agent/pi, scm/mr, preview, deploy, workflow/call, scan, tests, agent/review, guidance/conformance)", st.Name, s.Name, s.Uses)
			}
			if s.Uses == "approval" {
				if err := validateApproval(st.Name, s, strict); err != nil {
					return err
				}
			}
			if d, ok := s.With["docker"]; ok && s.Uses == "agent/pi" {
				if _, isBool := d.(bool); !isBool {
					return fmt.Errorf("stage %q step %q: docker must be true (a private Docker daemon for the agent) or false (got %v)", st.Name, s.Name, d)
				}
			}
		}
		if st.Gate != nil {
			for _, r := range st.Gate.Require {
				if !seenStep[r] {
					return fmt.Errorf("stage %q: gate requires unknown step %q", st.Name, r)
				}
			}
		}
	}
	return nil
}

// StageNames returns the ordered stage names of a spec.
func (s *Spec) StageNames() []string {
	out := make([]string, len(s.Stages))
	for i, st := range s.Stages {
		out[i] = st.Name
	}
	return out
}

// Stage returns the stage with the given name and its index, or nil, -1.
func (s *Spec) Stage(name string) (*Stage, int) {
	for i := range s.Stages {
		if s.Stages[i].Name == name {
			return &s.Stages[i], i
		}
	}
	return nil, -1
}

// UsesOldDockerRule: an agent step's with: that uses skill/implementation and
// says nothing of docker. Private Docker was given to such a step until steps
// declared it with docker: true.
func UsesOldDockerRule(with map[string]any) bool {
	_, declared := with["docker"]
	skills, _ := with["skills"].([]any)
	return !declared && slices.Contains(skills, any("skill/implementation"))
}

// validateApproval checks approval-step config the engine will later trust:
// quorum must be a whole number ≥ 1, and a quorum that names too few
// people to ever be met is rejected here rather than wedging a run. A
// group:<name> approver can hold any number of people, so its size is
// only knowable at decision time — quorum bounds are checked against
// named individuals only when no group is listed.
func validateApproval(stage string, s Step, strict bool) error {
	if strict {
		// The standard of approval: without a stated meaning, an OK proves
		// someone clicked, not what they vouched for — it's all just LGTM.
		means, _ := s.With["means"].(string)
		if strings.TrimSpace(means) == "" {
			return fmt.Errorf("stage %q step %q: an approval must say what an OK means here — add with: means: \"...\" (one sentence: what is the approver vouching for? e.g. \"I trust the agent did what it said\" or \"I read the code and it meets our norms\")", stage, s.Name)
		}
	}
	if s.With == nil {
		return nil
	}
	q, present := s.With["quorum"]
	if !present {
		return nil
	}
	n, ok := asInt(q)
	if !ok || n < 1 {
		return fmt.Errorf("stage %q step %q: quorum must be a whole number of people, 1 or more (got %v)", stage, s.Name, q)
	}
	apps, _ := s.With["approvers"].([]any)
	hasGroup := false
	for _, a := range apps {
		if name, ok := a.(string); ok && strings.HasPrefix(name, "group:") {
			hasGroup = true
		}
	}
	if !hasGroup && n > len(apps) {
		return fmt.Errorf("stage %q step %q: quorum of %d but only %d approvers named — it could never be met", stage, s.Name, n, len(apps))
	}
	return nil
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	}
	return 0, false
}
