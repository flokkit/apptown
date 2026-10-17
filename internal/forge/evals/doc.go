// Package evals answers "is this model profile, or this pack version,
// better or worse than what projects run now?" (design/subsystems/conformance.md,
// stock/docs/guides/running-a-forge.md). An eval re-runs up
// to five landed Changes in throwaway copies of their projects: one arm as
// the project runs now (the baseline) and one with the one thing
// substituted (the variant): the profile, or the candidate adoption the
// pack version would be for that project, model held. What each arm's copy
// adopts is fixed at start by digest; a setup that changed since is not
// comparable. A person reads the scorecard; it decides nothing and is
// never evidence.
//
// # The comparison
//
// Once an arm lands, and before its deployment is taken down, the driver
// has Verification judge the checks both arms declare on its landed main
// and on the Change's base (read once for the Change), finding by finding:
// new, fixed, already there, or not comparable with why. Model rubrics in
// it go through the judge's door ([Gate.AdmitJudge]): only the judge
// recorded for the Change at start (the baseline's fast model), for its
// copies, while the eval may proceed. The arm's own models do not grow.
//
// # Authority
//
// [Service.Start] is the one human act: a standards manager with project
// read on every source, shown [Meaning], starts it. Every run in a copy then
// carries the eval's landing authority, which Work's landing guard accepts
// only where the effect's project is a copy of that same eval. Only the
// starter continues a held eval. An administrator, or the starter while
// they still manage packs, deletes one; current grants are asked at the
// deletion. The driver acts in a copy as the "eval" instrument, bound to
// that copy.
//
// # The copy record
//
// Whether a project is a copy is Project's purpose (project.EvalCopy),
// written in the copy's own INSERT; every effect owner asks Project what
// that purpose allows. The copy record ([Copy]) is Evals' membership: which
// eval and arm made the copy. It is written in the same transaction and
// never edited or deleted, so complaints and spend stay attributed after the
// copy and the eval are gone. The gate, the arm's models and Work's landing
// authority read it by its key.
//
// # The driver
//
// [Service.Pass] passes over every running eval from what is recorded, so a
// restart picks up where it left off: at most [MaxRunning] arms at once. A
// pass is a job ([PassJob]) on its own one-worker queue, every minute and
// when an eval is started or continued, so passes never overlap.
// One rule, [Gate], says whether an eval's work may proceed, and it is
// asked at the effects, not at each door that leads to one: every model
// dispatch for a copy ([Gate.AdmitModel]: the forge's checks, narratives
// and briefs, and agents through the inference route), every step claim
// and every merge into a copy's main (Work's EvalGate). Whatever starts new
// work, its spending and its irreversible effects stop there. Entry points
// ask it too, where that saves wasted work: before each arm's copy is made
// (under the eval's row lock, with the copy record and the arm's link
// written in the same transaction, so a deletion either comes first and
// nothing is made or comes after and finds the copy), and again before its
// lane starts, when spend nobody could count holds the lane with its copy
// kept. A spent budget stops the eval (stopping): running
// arms are abandoned, and work Work will not stop stays live and observed
// until it ends. An incomplete spend observation holds the next arm until
// [Service.Continue]. Accepted overspend is the steps already running when
// the budget runs out, one per running arm.
package evals
