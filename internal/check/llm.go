package check

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// LLMEvaluatorVersion identifies the rubric and selector hints. Check sessions
// additionally identify their prompt, runtime contract and admitted model.
func LLMEvaluatorVersion(c Declaration) string {
	return "agent-check/v1/" + BundleHash(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t\x00evidenceUse=%s", c.Rubric, c.Expects, Scope(c), strings.Join(c.Files, "\x00"), c.SourceFiles, c.EvidenceUse))
}
func BundleHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Item is one isolated question, on a committed tree or an exact diff.
// Files are selector hints in the declaration, never packed evidence.
type Item struct {
	Declared
	Scope              string
	Repository, Commit string
	Recheck            bool
	Hash               string
}

type Verdict struct {
	Item     int    `json:"item,omitempty"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	Note     string `json:"note"`
}

func TreeItems(checks []Declared) []Item {
	var out []Item
	for _, c := range checks {
		if Evaluator(c.Declaration) == "llm" && Scope(c.Declaration) == ScopeTree {
			out = append(out, Item{Declared: c, Scope: ScopeTree})
		}
	}
	return out
}
func DiffItems(checks []Declared, diff string) []Item {
	var out []Item
	for _, c := range checks {
		if Evaluator(c.Declaration) == "llm" && Scope(c.Declaration) == ScopeDiff {
			out = append(out, Item{Declared: c, Scope: ScopeDiff, Hash: BundleHash(diff)})
		}
	}
	return out
}

func Judgement(it Item, v Verdict, answered bool, mode Mode) Evaluation {
	ev := Evaluation{Declared: it.Declared, Mode: mode, Evidence: evidence{EvaluatorVersion: LLMEvaluatorVersion(it.Declaration), Hash: it.Hash}}
	if !answered {
		ev.Outcome, ev.Detail = CannotAssess, "no verdict returned for this check"
		return ev
	}
	if it.EvidenceUse != "wording" && claimsOnly(v.Evidence) {
		ev.Outcome, ev.Detail = CannotAssess, "only project claims were cited; cite independent evidence of application behaviour"
		return ev
	}
	ev.Detail = strings.TrimSpace(v.Note + " [" + v.Evidence + "]")
	switch v.Verdict {
	case "pass":
		ev.Outcome = Conforms
	case "fail":
		if strings.TrimSpace(v.Evidence) == "" {
			ev.Outcome, ev.Detail = CannotAssess, "check failure has no citation"
			break
		}
		ev.Outcome = DoesNotConform
		ev.Findings = []Finding{conformanceFinding(it.Declared, ev.Detail, " (llm, findings-only)")}
	default:
		ev.Outcome = CannotAssess
	}
	return ev
}

// Citation paths are read independently of the bundle: a supplied verdict must
// obey the same claims rule as a model response. Keep this in Judgement so
// other evaluators can share the boundary.
var citationPath = regexp.MustCompile(`[A-Za-z0-9_./-]+\.[A-Za-z][A-Za-z0-9]*`)

// An extensionless file is read only as a whole citation token with a line,
// and only when its name starts with a capital letter (Dockerfile, Makefile,
// Gemfile): image references are lowercase and digests are sha256:<hex>, so
// python:3 or sha256:1910 in the evidence is never taken for a file.
var extensionlessCitation = regexp.MustCompile(`^/?(?:[A-Za-z0-9_.-]+/)*[A-Z][A-Za-z0-9_-]*:[0-9]+(?:-[0-9]+)?$`)

func claimsOnly(evidence string) bool {
	paths := citationPath.FindAllString(evidence, -1)
	for _, token := range strings.FieldsFunc(evidence, func(r rune) bool { return strings.ContainsRune(" \t\n\r;,()[]\"'", r) }) {
		token = strings.TrimRight(token, ".:")
		if extensionlessCitation.MatchString(token) {
			paths = append(paths, token[:strings.LastIndexByte(token, ':')])
		}
	}
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		switch path.Base(p) {
		case "PROJECT-NOTES.md", "AGENTS.md":
		default:
			return false
		}
	}
	return true
}
