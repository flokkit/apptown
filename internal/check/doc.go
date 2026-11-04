// Package check judges a declared check against a source tree.
//
// A check is a declared expectation — what must hold, how it is judged (sh,
// tool, llm, runtime), how severe a failure is, and whether it is about a
// state of the tree or a transition one change made. Evaluating one needs the
// declaration, a source tree and three capabilities: run a command in a
// container, ask a model a checklist, probe a URL. None of that is a project,
// an adoption, a Change or a review, so none of it is AppTown's: this package
// knows nothing of AppTown and could be lifted into another program
// (design/architecture/layout.md, "When something becomes a library"); scripts/shape-check.py's standalone rule holds it to that.
//
// Tree rubric evidence resolves the caller's tree root, then matches slash-based
// globs relative to it. ** spans zero or more directories; * stays in one.
// sourceFiles: true adds programming-language source in the union of applicable
// part folders (all declared folders for repository checks). It requires the
// caller's resolved contract context. Parts without a folder supply no source.
// go-enry v2.9.6 classifies full content using its language type data, excluding
// vendor, generated, documentation and binary files. Part tools do not narrow
// languages. NotAppCode folders are excluded. Unknown files are reported as
// unclassified, never as proof of absent code. Explicit files remain independent
// context selectors, including documentation. No project code is executed.
// Files are deduplicated and sorted by relative path. Dependency, build and
// runtime directories in skipDirs are skipped unless an exact path names a
// file there. Nested symlinks, special files, NUL-containing binaries,
// minified files and source maps are excluded. There is no file-count cap.
// Each file contributes at most 12 KiB of content, with a truncation marker;
// rubric bundles fit within 48 KiB and the batch budget is 160 KiB. Later
// sorted files and later declared rubrics lose evidence first. Item records
// both included files and the eligible match count before budget omissions.
//
// AppTown subsystem internal/apptown/conformance wraps records and decisions
// around it: which declaration a project adopted, whose exception excuses a
// finding, what the gate decides over head and base, and what a review recorded.
package check
