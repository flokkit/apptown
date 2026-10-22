package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// A check's declaration survives scanner expansion. Only identity is recorded
// in the stage event; execution configuration and credentials stay on steps.
type Check struct {
	Declaration string `json:"declaration"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

type plannedStep struct {
	check      Check
	configJSON string
	onEnter    bool
}

// planStage is deterministic over the declaration and scanner snapshot.
// Encode private configurations before any write; malformed inputs cannot leave
// a partial stage, and callers cannot mutate the plan through input maps.
func planStage(stage *schema.Stage, scanners []Scanner) ([]plannedStep, error) {
	var plan []plannedStep
	names := map[string]bool{}
	add := func(declaration, name, kind string, config json.RawMessage, onEnter bool) error {
		if name == "" || names[name] {
			return fmt.Errorf("stage %q has missing or duplicate concrete check %q", stage.Name, name)
		}
		names[name] = true
		if len(config) == 0 || string(config) == "null" {
			config = json.RawMessage(`{}`)
		}
		plan = append(plan, plannedStep{Check{declaration, name, kind}, string(config), onEnter})
		return nil
	}
	all := append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...)
	for i, step := range all {
		onEnter := i < len(stage.OnEnter)
		// A person's acceptance on retrying a deploy is never data a
		// workflow declares (RetryCommand.AcceptUncopiedData).
		// JSON reads keys in any casing, so every casing is refused.
		for k := range step.With {
			if strings.EqualFold(k, uncopiedDataKey) {
				return nil, fmt.Errorf("check %q declares %s: that is a person's acceptance on retrying a deploy, which no workflow may declare", step.Name, k)
			}
		}
		// The schema owns extension fields; freeze exact JSON before expanding them.
		encoded, err := json.Marshal(step.With)
		if err != nil {
			return nil, fmt.Errorf("check %q configuration: %w", step.Name, err)
		}
		target := scanTarget(step)
		if target == "" {
			if err := add(step.Name, step.Name, step.Uses, encoded, onEnter); err != nil {
				return nil, err
			}
			continue
		}
		matched := false
		for _, scanner := range scanners {
			if scanner.Target != target {
				continue
			}
			config := map[string]json.RawMessage{}
			if err := json.Unmarshal(encoded, &config); err != nil {
				return nil, err
			}
			delete(config, "on")
			config["scanner"], err = json.Marshal(scanner.Name)
			if err != nil {
				return nil, err
			}
			body, err := json.Marshal(config)
			if err != nil {
				return nil, err
			}
			if err := add(step.Name, step.Name+":"+scanner.Name, "scan", body, onEnter); err != nil {
				return nil, err
			}
			matched = true
		}
		if !matched {
			body, err := json.Marshal(struct {
				On   string `json:"on"`
				None bool   `json:"none"`
			}{target, true})
			if err != nil {
				return nil, err
			}
			if err := add(step.Name, step.Name, "scan", body, onEnter); err != nil {
				return nil, err
			}
		}
	}
	return plan, nil
}

func scanTarget(step schema.Step) string {
	if step.Uses != "scan" {
		return ""
	}
	target, _ := step.With["on"].(string)
	return target
}

type stageGate struct {
	Required []string
	Missing  []string
	Blocking []*Step
}

func (g stageGate) Passed() bool { return len(g.Missing) == 0 && len(g.Blocking) == 0 }

// permitsMerge admits the effect that can establish the SCM proof. It does not
// waive missing declarations, scanner receipts, or any other required check.
// Workflow advancement still requires Passed(), including the resulting proof.
func (g stageGate) PermitsMerge() bool {
	if len(g.Missing) != 0 {
		return false
	}
	for _, step := range g.Blocking {
		if step.Type != "scm/merged" {
			return false
		}
	}
	return true
}

// evaluateGate never interprets absent evidence as a successful check.
// Scanner groups require their persisted expansion; no name-prefix guessing or
// lookup of today's attached scanners is permitted here.
func evaluateGate(stage *schema.Stage, recorded []Check, steps []*Step) (stageGate, error) {
	var result stageGate
	declared := map[string]schema.Step{}
	for _, s := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
		declared[s.Name] = s
	}
	expanded := map[string][]Check{}
	recordedNames := map[string]bool{}
	for _, c := range recorded {
		d, ok := declared[c.Declaration]
		if !ok || c.Name == "" || c.Type != d.Uses || recordedNames[c.Name] || (scanTarget(d) == "" && c.Name != d.Name) {
			return result, fmt.Errorf("stage %q has an invalid check receipt for %q", stage.Name, c.Name)
		}
		recordedNames[c.Name] = true
		expanded[c.Declaration] = append(expanded[c.Declaration], c)
	}
	required := map[string]bool{}
	explicit := stage.Gate != nil && len(stage.Gate.Require) > 0
	if explicit {
		for _, name := range stage.Gate.Require {
			required[name] = true
		}
	} else {
		for name := range declared {
			required[name] = true
		}
	}
	for name := range required {
		result.Required = append(result.Required, name)
	}
	sort.Strings(result.Required)
	byName := map[string][]*Step{}
	for _, s := range steps {
		byName[s.Name] = append(byName[s.Name], s)
	}
	checked := map[*Step]bool{}
	judge := func(s *Step) {
		if !s.Status.PassesGate() {
			result.Blocking = append(result.Blocking, s)
		}
	}
	check := func(c Check) {
		matches := byName[c.Name]
		if len(matches) != 1 || matches[0].Type != c.Type {
			result.Missing = append(result.Missing, c.Name)
			return
		}
		checked[matches[0]] = true
		judge(matches[0])
	}
	for _, name := range result.Required {
		d, ok := declared[name]
		if !ok {
			result.Missing = append(result.Missing, name)
			continue
		}
		if scanTarget(d) == "" {
			check(Check{name, name, d.Uses})
		} else if len(expanded[name]) == 0 {
			result.Missing = append(result.Missing, name+" (expected scanners were not recorded)")
		} else {
			for _, c := range expanded[name] {
				check(c)
			}
		}
	}
	if !explicit {
		// Dynamic law/merge checks still gate the default all-checks stage. They
		// cannot satisfy a missing declared check, even if all survivors pass.
		for _, s := range steps {
			if checked[s] {
				continue
			}
			if _, ok := declared[s.Name]; !ok && !recordedNames[s.Name] {
				result.Required = append(result.Required, s.Name)
			}
			judge(s)
		}
	}
	sort.Strings(result.Required)
	sort.Strings(result.Missing)
	return result, nil
}

type stageSuccessor struct {
	Stage             string
	Completing, Merge bool
}

func nextStage(spec *schema.Spec, current string, child bool) (stageSuccessor, error) {
	_, idx := spec.Stage(current)
	if idx < 0 {
		return stageSuccessor{}, fmt.Errorf("current stage %q not in definition", current)
	}
	next := stageSuccessor{Completing: idx == len(spec.Stages)-1}
	if !next.Completing {
		next.Stage = spec.Stages[idx+1].Name
	}
	next.Merge = !child && ((next.Completing && spec.Role != "working" && spec.Role != "record") || (!next.Completing && idx+1 == len(spec.Stages)-1 && spec.Role == "landing"))
	return next, nil
}
