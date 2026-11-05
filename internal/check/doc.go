// Package check judges a declared check against a source tree.
//
// A check is a declared expectation — what must hold, how it is judged (sh,
// tool, llm, runtime), how severe a failure is, and whether it is about a
// state of the tree or a transition one change made. Evaluating one needs the
// declaration, a source tree and three capabilities: run a command in a
// container, invoke one agent check, probe a URL. None of that is a project,
// an adoption, a Change or a review, so none of it is AppTown's: this package
// knows nothing of AppTown and could be lifted into another program
// (design/architecture/layout.md, "When something becomes a library"); scripts/shape-check.py's standalone rule holds it to that.
//
// AI rubrics are independent agent invocations supplied by the caller. Files
// and sourceFiles are exploration hints; this library neither packs excerpts
// nor performs classifier-based source selection. A missing agent capability
// reports cannot-assess. Committed checkout isolation, model access, limits,
// transcript retention and exact-input reuse belong to the invocation owner.
//
// AppTown subsystem internal/apptown/conformance wraps records and decisions
// around it: which declaration a project adopted, whose exception excuses a
// finding, what the gate decides over head and base, and what a review recorded.
package check
