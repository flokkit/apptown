// Package trailer is the one list of the trailer keys Forge writes on the
// commits it makes, and the one way to write them. Commits leave the forge
// (they are pushed upstream and read by people and tools that never see the
// forge's database), so their keys are a format: one list, one case (each
// word capitalised), changed only by adding. Commits already written keep the
// keys they were written with.
package trailer

import "strings"

// The keys. A reader that finds commits by a key (scm finds a landing's
// merge by Operation) must use the constant, so the spelling cannot drift.
const (
	RunID            = "Forge-Run-Id"            // the run whose work the commit is
	Step             = "Forge-Step"              // the step that made it
	Workflow         = "Forge-Workflow"          // name@vN the run was admitted under
	Operation        = "Forge-Operation"         // the landing operation; found again by it
	ChangeID         = "Forge-Change-Id"         // the change the commit belongs to
	ChangePage       = "Forge-Change-Page"       // where the change's record is read
	OpenedBy         = "Forge-Opened-By"         // who opened the change, as its receipt says
	ApprovedBy       = "Forge-Approved-By"       // a recorded approving vote and its stated meaning
	EvalAuthorised   = "Forge-Eval-Authorised"   // a step an eval authorised instead of a person
	LandingAuthority = "Forge-Landing-Authority" // the authority recorded when the landing was admitted
	Override         = "Forge-Override"          // a step overridden, by whom and why
	Actor            = "Forge-Actor"             // who made a commit the forge writes for itself
	Provenance       = "Forge-Provenance"        // what kind of forge writer made it
)

// Keys is every key, for the test that refuses one written outside this list.
var Keys = []string{RunID, Step, Workflow, Operation, ChangeID, ChangePage, OpenedBy, ApprovedBy, EvalAuthorised, LandingAuthority, Override, Actor, Provenance}

// Line is one trailer: its key and its value on one line.
type Line struct{ Key, Value string }

// Block writes trailers as Git reads them: one "Key: value" per line, each
// value folded onto one line so no value can start a trailer of its own.
func Block(lines ...Line) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Key+": "+strings.Join(strings.Fields(l.Value), " "))
	}
	return strings.Join(out, "\n")
}

// Message is a commit message: its subject, an optional body, and the
// trailer block, each separated by a blank line.
func Message(subject, body string, lines ...Line) string {
	parts := []string{subject}
	if body != "" {
		parts = append(parts, body)
	}
	if len(lines) > 0 {
		parts = append(parts, Block(lines...))
	}
	return strings.Join(parts, "\n\n")
}
