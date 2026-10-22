// Package conformance owns reviews, recorded assessments, exception decisions,
// confirmations, fixes and the current/historical projections over them.
//
// Service is the supported application API. Its stores, SQL and row decoders
// are private. Authority resolves authenticated operation context and checks
// current capability/scope; command fields never confer authority. AppTown binds
// that port to its existing access kernel and supplies committed source through
// Source. Neither port receives App or a general collection of collaborators.
//
// AdmitReviewIn and AdmitWhatIfIn save a queued record with its request in
// the caller's transaction, which also queues the job that runs it;
// ClaimReview and ClaimWhatIf start it for the one delivery that claims it,
// from the saved request alone. BeginReview admits a review its caller runs
// at once (remediation).
//
// BeginReview and ClaimReview return an admitted Execution. It owns the selected declarations,
// committed source identities and historical exception snapshots. Evaluation and
// Git attribution happen outside transactions. PendingVerdict.Commit rechecks
// terminal state, the preceding verdict and required confirmation before saving
// the assessment, fix and required receipt atomically. Admission has no token
// expiry timer: a long evaluation may finish, but a closed review rejects writes.
//
// ReviewState and Outcome are distinct owned types. ReviewState.Transition is
// the lifecycle rule; projections may report unassessed or stale observations
// without writing those display states as verdicts. Required read/write failures
// are errors, never absence, an empty successful receipt or a guessed source.
//
// RepairInterruptedReviews and RepairInterruptedWhatIfs are named participants
// in the caller's startup transaction. Startup invokes them before
// request/worker admission; they end queued and running records and do not
// restart evaluation. Interrupted reviews require explicit reruns.
package conformance
