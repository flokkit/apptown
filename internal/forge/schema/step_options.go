package schema

import (
	"fmt"
	"slices"
	"strings"
)

// stepOptions is each step kind's authored options: the keys a saved
// workflow may put in a step's with. A save refuses any other key by name,
// so a misspelt or invented option (minApprovals for quorum) is an error
// rather than a key the runner never reads. The runners that decode each
// kind hold their configuration types to this list in their own tests
// (they import this package, so it cannot ask them).
var stepOptions = map[string][]string{
	"agent/pi":             {"prompt", "model", "thinking", "executor", "image", "scm", "skills", "resources", "requires", "services", "needs", "docker", "mayChangeNothing", "output", "timeoutMinutes"},
	"agent/review":         {"of", "prompt", "question", "model", "thinking", "timeoutMinutes"},
	"approval":             {"approvers", "quorum", "means", "instructions", "inAdvance"},
	"artifact/require":     {"kind", "name"},
	"adr/require":          {"status", "tags"},
	"preview":              {"services"},
	"deploy":               {"deployment", "services"},
	"tests":                {"command", "require", "timeoutMinutes"},
	"scan":                 {"on", "scanner", "failOn", "timeoutMinutes"},
	"guidance/conformance": {"failOn"},
	"workflow/call":        {"workflow", "with"},
	"script":               {"run", "env", "executor", "image", "timeoutMinutes"},
	"reconcile":            {},
	"scm/mr":               {},
}

// writtenByAppTown is the keys AppTown itself writes into a step's stored
// configuration, by kind: how a person retried it, or what an empty scan
// point expanded to. They are recorded, never authored.
var writtenByAppTown = map[string][]string{
	"agent/pi": {"retryMode", "continueSession", "sendback"},
	"scan":     {"none"},
	"deploy":   {"acceptUncopiedData"}, // a person's acceptance on retrying it
}

// StepOptions is the options a saved workflow may give a step of kind, and
// whether the kind is one AppTown runs.
func StepOptions(kind string) ([]string, bool) {
	o, ok := stepOptions[kind]
	return slices.Clone(o), ok
}

// WrittenByAppTown is the keys AppTown records in a stored step of kind
// that no workflow may declare.
func WrittenByAppTown(kind string) []string { return slices.Clone(writtenByAppTown[kind]) }

// checkOptions refuses a with key the step's kind does not take. Keys match
// exactly: a runner's JSON decoding would read Quorum as quorum, so any
// other casing is refused here, not read there.
func checkOptions(stage string, s Step) error {
	allowed, _ := StepOptions(s.Uses)
	recorded := WrittenByAppTown(s.Uses)
	for k := range s.With {
		if slices.Contains(allowed, k) {
			continue
		}
		if slices.Contains(recorded, k) {
			return fmt.Errorf("stage %q step %q: %s is recorded by AppTown when it runs the step; a workflow does not declare it", stage, s.Name, k)
		}
		if len(allowed) == 0 {
			return fmt.Errorf("stage %q step %q: %s takes no options (got %s)", stage, s.Name, s.Uses, k)
		}
		return fmt.Errorf("stage %q step %q: %s has no option %s (options: %s)", stage, s.Name, s.Uses, k, strings.Join(allowed, ", "))
	}
	if v, ok := s.With["failOn"]; ok {
		if severity, isText := v.(string); !isText || !slices.Contains(FailOnValues, severity) {
			return fmt.Errorf("stage %q step %q: failOn %v is not a severity (%s)", stage, s.Name, v, strings.Join(FailOnValues, ", "))
		}
	}
	return nil
}

// FailOnValues are the severities a step's failOn may name. A typo would
// otherwise gate at a threshold nobody chose, so a save refuses it; leaving
// failOn out is its own setting (the step kind's default).
var FailOnValues = []string{"low", "medium", "high", "critical"}
