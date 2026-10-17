package conformance

import "flokkit.io/forge/internal/check"

// What a check DECLARES — what must hold, how it is judged, how severe, and
// whether it is about a state of the tree or a transition — is the library's
// (internal/check): none of it is a project, an adoption or a review. What
// conformance adds is the EXPECTATION: a declaration a project adopted, at a
// block version, which is what a finding's provenance names.

type CheckDeclaration = check.Declaration

// Check scopes.
const (
	ScopeTree = check.ScopeTree
	ScopeDiff = check.ScopeDiff
)

// DeclaredCheck ties a check to the block version that declares it — the
// provenance every finding must name.
type DeclaredCheck struct {
	Block   string
	Version int
	Check   CheckDeclaration
}

// Declared is the expectation as the library sees it: a declaration under an
// identity it makes nothing of but the fingerprint.
func (sc DeclaredCheck) Declared() check.Declared {
	return check.Declared{Identity: check.Identity{Namespace: sc.Block, Version: sc.Version}, Declaration: sc.Check}
}

// CheckDefinition is a check made legible: what it requires (Expects, plain English),
// HOW it is evaluated (the actual sh/rubric/tool/probe), WHERE it is judged
// (its scope), its severity, and which projects adopt it. Keyed like byCheck
// (block@version·check) so the UI can join a verdict list to its definition.
type CheckDefinition struct {
	Title       string   `json:"title,omitempty"`
	Key         string   `json:"key"`
	Block       string   `json:"block"`
	Version     int      `json:"version"`
	Check       string   `json:"check"`
	Expects     string   `json:"expects"`
	Evaluator   string   `json:"evaluator"`
	Severity    string   `json:"severity"`
	Sh          string   `json:"sh,omitempty"`
	Rubric      string   `json:"rubric,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Probe       string   `json:"probe,omitempty"`
	Scope       string   `json:"scope"`
	Files       []string `json:"files,omitempty"`
	JudgedWhere string   `json:"judgedWhere"`
	Adopters    []string `json:"adopters"`
}

// DefinitionOf builds a catalog entry from a check definition.
func DefinitionOf(block string, version int, c CheckDeclaration) CheckDefinition {
	severity := c.Severity
	if severity == "" {
		severity = "note"
	}
	return CheckDefinition{
		Key: CheckKey(block, version, c.Name), Block: block, Version: version, Check: c.Name,
		Title: c.Title, Expects: c.Expects, Evaluator: check.Evaluator(c), Severity: severity,
		Sh: c.Sh, Rubric: c.Rubric, Tool: c.Tool, Probe: c.Probe,
		Scope: check.Scope(c), Files: c.Files, JudgedWhere: check.JudgedWhere(c),
	}
}

// Fingerprint is the stable finding identity shared by gates and review exceptions.
func Fingerprint(sc DeclaredCheck, file string) string {
	return check.Fingerprint(sc.Declared().Identity, sc.Check.Name, file)
}
