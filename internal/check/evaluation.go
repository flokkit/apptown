package check

import (
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/strs"
)

// Mode is what the evaluation is asked to say: a Gate judges one change's
// proposed tree, a Posture sweep judges a committed tree as it stands.
type Mode int

const (
	Gate Mode = iota
	Posture
)

func (m Mode) String() string {
	if m == Posture {
		return "posture"
	}
	return "gate"
}

// Outcome is what a judge can say on its own. A person's decisions over an
// outcome — excepted, not applicable — are the caller's, never the library's.
type Outcome int

const (
	Conforms Outcome = iota + 1
	DoesNotConform
	CannotAssess // not mechanically answerable, or the source lacks the capability
	Error        // the evaluator failed, timed out, or returned malformed output
)

func (o Outcome) String() string {
	switch o {
	case Conforms:
		return "conforms"
	case DoesNotConform:
		return "does-not-conform"
	case CannotAssess:
		return "cannot-assess"
	case Error:
		return "error"
	}
	return "invalid-outcome"
}

// Tokens is what a model call cost, as the judge observed it. What that means
// in money, and whose budget it lands on, is the caller's business.
type Tokens struct {
	Input  int64
	Output int64
}

// Evidence is what the judge recorded about HOW it judged: the evaluator's own
// identity, the hash of the exact material it looked at, and its output.
type Evidence struct {
	EvaluatorVersion string // the identity a verdict keys on; unchanged strings
	Hash             string // hash of the evidence bundle judged
	PromptHash       string // model calls only
	Report           string // the report a tool wrote and the judge read; "" = none
	Image            string // the image (by ID) an exec-image check ran in; "" = none
	Files            []string
	Output           string
	ExitCode         *int
	Duration         time.Duration
	Tokens           Tokens
}

// Evaluation is one declaration against one source in one mode.
type Evaluation struct {
	Declared
	Mode        Mode
	Outcome     Outcome
	Detail      string
	Findings    []Finding
	Occurrences []string
	Evidence    Evidence
	// Fault: the judge failed, not the tree. A change must never be sent back
	// to its author over one.
	Fault bool
}

// Source is the material an evaluation judges. A capability the source lacks
// is cannot-assess with the reason, never an error.
type Source struct {
	Tree  string // a read-only checkout of the exact commit; the caller stages it
	SHA   string
	Diff  string // for diff-scoped rubrics; "" in a review
	Image string // built app image reference for exec-image tools; "" = not available
	// Env is what an exec-image tool runs with: the environment of a deployed
	// app, derived by the caller from what the app declares.
	Env []string
	App string // base URL of a booted app for runtime probes; "" = not available
	// Naked: the same app reached by address with no front-door signal. "" when
	// only one door is reachable.
	Naked string
}

// conformanceFinding is the finding a failing declared check reports: the
// check's own words, under the fingerprint a disposition matches on.
func conformanceFinding(d Declared, detail, suffix string) Finding {
	return Finding{
		Fingerprint: Fingerprint(d.Identity, d.Name, ""),
		Kind:        "conformance",
		Severity:    d.Severity,
		Title:       fmt.Sprintf("guidance %s v%d · %s%s", d.Namespace, d.Version, d.Name, suffix),
		Detail:      strs.FirstNonBlank(detail, d.Expects),
		Source:      "guidance",
	}
}
