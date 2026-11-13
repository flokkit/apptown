package schema

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/check"
)

// stepType describes a step's authored options and execution classifications.
type stepType struct {
	Authored      bool
	Options       []string
	Check         bool
	CheckExecutor bool
}

// StepTypes is the catalogue consumed by validation, editors and check readers.
// Internal steps may count as checks without being valid authored workflow steps.
var StepTypes = map[string]stepType{
	"agent/pi":             {Authored: true, Options: []string{"prompt", "model", "thinking", "executor", "image", "scm", "skills", "resources", "requires", "services", "needs", "docker", "mayChangeNothing", "repairRounds", "output", "timeoutMinutes"}},
	"agent/review":         {Authored: true, Options: []string{"of", "prompt", "question", "model", "thinking", "timeoutMinutes"}, Check: true},
	"approval":             {Authored: true, Options: []string{"approvers", "quorum", "means", "instructions", "inAdvance", "standing"}},
	"artifact/require":     {Authored: true, Options: []string{"kind", "name"}},
	"adr/require":          {Authored: true, Options: []string{"status", "tags"}},
	"preview":              {Authored: true, Options: []string{"services"}},
	"build":                {Authored: true, Options: []string{}},
	"tests":                {Authored: true, Options: []string{"require", "timeoutMinutes", "appliesTo"}, Check: true, CheckExecutor: true},
	"scan":                 {Authored: true, Options: []string{"on", "scanner", "baseline", "failOn", "timeoutMinutes"}, Check: true, CheckExecutor: true},
	"guidance/conformance": {Authored: true, Options: []string{"failOn"}, Check: true, CheckExecutor: true},
	"workflow/call":        {Authored: true, Options: []string{"workflow", "with", "whenFailed", "findings"}},
	"fix":                  {Authored: true, Options: []string{"kind", "for"}},
	"routine":              {Authored: true, Options: []string{}, Check: true, CheckExecutor: true},
	"script":               {Authored: true, Options: []string{"run", "env", "executor", "image", "timeoutMinutes"}, Check: true},
	"reconcile":            {Authored: true, Options: []string{}},
	"scm/mr":               {Authored: true, Options: []string{}},
	"scm/merged":           {Check: true},
}

// writtenByAppTown is the keys AppTown itself writes into a step's stored
// configuration, by kind: how a person retried it, or what an empty scan
// point expanded to. They are recorded, never authored.
var writtenByAppTown = map[string][]string{
	"agent/pi": {"retryMode", "continueSession", "sendback"},
	"scan":     {"none"},
}

// StepOptions is the options a saved workflow may give a step of kind, and
// whether the kind may be authored.
func StepOptions(kind string) ([]string, bool) {
	t, ok := StepTypes[kind]
	return slices.Clone(t.Options), ok && t.Authored
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
	if v, ok := s.With["appliesTo"]; ok {
		// The tests step's: the parts whose having no test command is a
		// failure rather than not applicable, in the checks' words.
		words, isList := v.([]any)
		var ws []string
		for _, w := range words {
			text, isText := w.(string)
			if !isText {
				isList = false
			}
			ws = append(ws, text)
		}
		if !isList {
			return fmt.Errorf("stage %q step %q: appliesTo is a list of words (tool:go, build, ...)", stage, s.Name)
		}
		if err := check.ValidateAppliesTo(ws); err != nil {
			return fmt.Errorf("stage %q step %q: %w", stage, s.Name, err)
		}
	}
	if v, ok := s.With["baseline"]; ok && v != "none" {
		return fmt.Errorf("stage %q step %q: baseline must be none", stage, s.Name)
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
