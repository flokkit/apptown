package check

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Declaration is what must hold and how it is judged. The JSON tags are the
// authored form: a way of working carries its checks in this shape.
type Declaration struct {
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`     // authored action title for a remediation; identity remains Name
	Expects   string `json:"expects"`             // one plain sentence: what conformance looks like
	Severity  string `json:"severity,omitempty"`  // note|low|medium|high (default note)
	Evaluator string `json:"evaluator,omitempty"` // sh|llm|runtime|tool (default sh when sh set, llm when rubric set, tool when tool set)
	Sh        string `json:"sh,omitempty"`
	Rubric    string `json:"rubric,omitempty"`
	Probe     string `json:"probe,omitempty"` // runtime evaluator: a probe against the booted app
	Tool      string `json:"tool,omitempty"`  // tool evaluator: names a scanner binding to run (design/subsystems/conformance.md §13)
	// Scope says what the check is ABOUT (design/subsystems/conformance-surface.md §2):
	// "tree" — a state of the committed tree, judged at the gate AND in every
	// fleet review; "diff" — a transition made by one change, judged at the gate
	// only. sh/tool/runtime are always tree. An llm rubric defaults to diff (the
	// meaning every existing block version has) and may declare tree, in which
	// case Files names the evidence the rubric is judged on.
	Scope string   `json:"scope,omitempty"`
	Files []string `json:"files,omitempty"`
	// Occurrences says how the check reports WHAT it found: "lines" — one
	// occurrence per output line, so a gate can tell a pre-existing finding
	// from a new one (design/subsystems/ways-of-working-versions.md §4.2). Unset: the
	// check is boolean; failing on both trees is "regression unknown".
	Occurrences string `json:"occurrences,omitempty"`
}

// Check scopes.
const (
	ScopeTree = "tree"
	ScopeDiff = "diff"
)

// Identity names whoever declared a check, opaquely: AppTown passes the block
// and the version the project adopted. The library only prints it.
type Identity struct {
	Namespace string
	Version   int
}

// Declared is one declaration with the identity that declares it.
type Declared struct {
	Identity
	Declaration
}

// Fingerprint is the stable finding identity shared by gates and review
// exceptions. Dispositions on a live instance match on it: it does not change.
func Fingerprint(id Identity, check, file string) string {
	return fmt.Sprintf("guidance|%s@%d|%s|%s", id.Namespace, id.Version, check, file)
}

// Scope resolves a check's scope with the defaults of §2.1.
func Scope(c Declaration) string {
	if Evaluator(c) == "llm" {
		if c.Scope == ScopeTree {
			return ScopeTree
		}
		return ScopeDiff
	}
	return ScopeTree
}

// JudgedWhere says, in words, where a check is judged — the catalog shows it.
// JudgedAtLanding says whether a landing can judge the check, so a change
// can be accepted on it. A runtime check answered only by a standing
// deployment's steady use is skipped at every landing: no change can prove
// it, and it is never a change's acceptance.
func JudgedAtLanding(c Declaration) bool {
	if !IsRuntime(c) {
		return true
	}
	ps, err := ParseProbe(c.Probe)
	return err != nil || !ps.Standing()
}

func JudgedWhere(c Declaration) string {
	if Scope(c) == ScopeDiff {
		return "at landing only"
	}
	if IsRuntime(c) {
		if ps, err := ParseProbe(c.Probe); err == nil && ps.Standing() {
			return "in every review, on the running deployment"
		}
	}
	return "at landing and in every review"
}

// Evaluators is every evaluator kind a declaration may name. Validate admits
// exactly these, so a caller that judges declared checks can be held to
// judging each kind.
func Evaluators() []string { return []string{"sh", "llm", "runtime", "tool"} }

// Validate normalizes and checks a set of declarations, filling the defaults
// (evaluator, scope, severity) in place.
func Validate(checks []Declaration) error {
	seen := map[string]bool{}
	for i := range checks {
		c := &checks[i]
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("check %d: name required", i)
		}
		if seen[c.Name] {
			return fmt.Errorf("check %q: duplicate name", c.Name)
		}
		seen[c.Name] = true
		c.Evaluator = Evaluator(*c)
		if kinds := Evaluators(); !slices.Contains(kinds, c.Evaluator) {
			return fmt.Errorf("check %q: evaluator must be %s or %s", c.Name, strings.Join(kinds[:len(kinds)-1], ", "), kinds[len(kinds)-1])
		}
		switch c.Evaluator {
		case "sh":
			if strings.TrimSpace(c.Sh) == "" {
				return fmt.Errorf("check %q: sh evaluator requires a script", c.Name)
			}
		case "llm":
			if strings.TrimSpace(c.Rubric) == "" {
				return fmt.Errorf("check %q: llm evaluator requires a rubric", c.Name)
			}
		case "runtime":
			if strings.TrimSpace(c.Probe) == "" {
				return fmt.Errorf("check %q: runtime evaluator requires a probe", c.Name)
			}
		case "tool":
			if strings.TrimSpace(c.Tool) == "" {
				return fmt.Errorf("check %q: tool evaluator requires a tool (scanner binding) name", c.Name)
			}
		}
		if c.Occurrences != "" && c.Occurrences != "lines" {
			return fmt.Errorf("check %q: occurrences must be lines or absent", c.Name)
		}
		// Scope (§2.1): deterministic evaluators are state checks by nature; a
		// rubric says which it is, and a tree rubric must name its evidence.
		switch c.Evaluator {
		case "llm":
			switch c.Scope {
			case "", ScopeDiff:
				c.Scope = ScopeDiff
			case ScopeTree:
				if len(c.Files) == 0 {
					return fmt.Errorf("check %q: a tree-scoped rubric must list the files it is judged on", c.Name)
				}
			default:
				return fmt.Errorf("check %q: scope must be tree or diff", c.Name)
			}
		default:
			if c.Scope != "" && c.Scope != ScopeTree {
				return fmt.Errorf("check %q: a %s check is always tree-scoped (it has no before/after)", c.Name, c.Evaluator)
			}
			c.Scope = ScopeTree
		}
		if c.Severity == "" {
			c.Severity = "note"
		}
		switch c.Severity {
		case "note", "low", "medium", "high":
		default:
			return fmt.Errorf("check %q: severity must be note, low, medium or high", c.Name)
		}
	}
	return nil
}

// PostureScript returns an expectation's committed-tree sh script and whether
// it supports posture mode at all. An llm-only rubric has no posture mode.
func PostureScript(c Declaration) (string, bool) {
	if Evaluator(c) == "sh" && strings.TrimSpace(c.Sh) != "" {
		return c.Sh, true
	}
	return "", false
}

// IsPostureSh / IsRuntime classify an expectation's evaluation mode.
func IsPostureSh(c Declaration) bool { _, ok := PostureScript(c); return ok }

func IsRuntime(c Declaration) bool {
	return Evaluator(c) == "runtime" && strings.TrimSpace(c.Probe) != ""
}

func ModeFor(c Declaration) string {
	if IsRuntime(c) {
		return "runtime"
	}
	return "posture"
}

// Evaluator is the single default rule used by validation, descriptions and
// execution. Explicit declarations win; inferred declarations retain publication
// precedence (script, rubric, scanner, runtime probe).
func Evaluator(c Declaration) string {
	if c.Evaluator != "" {
		return c.Evaluator
	}
	switch {
	case c.Sh != "":
		return "sh"
	case c.Rubric != "":
		return "llm"
	case c.Tool != "":
		return "tool"
	case c.Probe != "":
		return "runtime"
	default:
		return "sh"
	}
}

// ParseDeclarations reads authored check declarations (a JSON array) and
// validates them: a key a declaration does not have is refused, so a
// misspelt field is an error, never a check silently judged without it.
// Stored declarations were read this way when saved.
func ParseDeclarations(raw string) ([]Declaration, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var cs []Declaration
	if err := dec.Decode(&cs); err != nil {
		return nil, fmt.Errorf("checks: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("checks: more than one JSON value")
	}
	if err := Validate(cs); err != nil {
		return nil, err
	}
	return cs, nil
}
