// Package check judges a declared check against a source tree.
//
// A check is a declared expectation — what must hold, how it is judged (sh,
// tool, llm, runtime), how severe a failure is, and whether it is about a
// state of the tree or a transition one change made. Evaluating one needs the
// declaration, a source tree and three capabilities: run a command in a
// container, ask a model a checklist, probe a URL. None of that is a project,
// an adoption, a Change or a review, so none of it is forge's: this package
// knows nothing of forge and could be lifted into another program
// (design/architecture/layout.md, "When something becomes a library"). cmd/apptown-check is the proof that it stands alone.
//
// The forge subsystem internal/forge/conformance wraps records and decisions
// around it: which declaration a project adopted, whose exception excuses a
// finding, what the gate decides over head and base, and what a review recorded.
package check
