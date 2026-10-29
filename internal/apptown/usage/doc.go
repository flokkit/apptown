// Package usage stores observed model usage. [Service] exposes [Ledger] and
// [Requests], the spend lines of AppTown's own model calls; both write
// token_usage rows, read by the same reports.
//
// # Observed usage
//
// Execution and conversation owners read harness output and submit attributed
// [Entry] and [Totals] values to [Ledger.Record]. The ledger appends observations
// that spent something, and observations whose spend is not wholly known
// (marked incomplete with a reason, even at zero), so unknown spend is never
// read as nothing; report totals count the incomplete rows, and a nonzero
// count makes them "at least". It does not read transcripts, verify attribution or deduplicate
// repeated submissions. [Ledger.Cursor] and [Ledger.SetCursor] track how far a
// transcript has been consumed, separately from appending its usage. Callers own
// coordination and replay handling across those operations.
//
// [Ledger.RunTotal] reports one or more run trees (a run, or a change's runs). [Ledger.Summary] supplies time-windowed
// project, workflow and instance reports for callers that have authorized the
// scope. [Service.Report] is the caller-facing read: it authorizes a named
// project, and otherwise covers the instance for an instance reader or only
// the caller's readable projects. Costs are observed estimates stored in
// micro-dollars, not reconciled provider invoices. Token/cache categories retain
// provider semantics and can overlap.
//
// # AppTown's own model calls
//
// For a call AppTown makes for itself (inference.Gateway.Ask),
// [Requests.OpenCall] writes a pending spend line before anything is sent,
// and [Requests.SettleCall] settles the line, only while it is pending and its
// time has not run out. A pending line counts nothing until its time runs out, and then
// counts as unknown spend. [Ledger.LockInUnknownIn] turns expired pending
// lines into unknown lines that can never change, under their row locks, and
// counts the unknown lines: an eval's acknowledgement saved from that count
// covers only lines that can never become known, so it never falls and never
// covers a later unknown charge. A settlement that comes after a lock-in
// changes nothing.
//
// Startup calls [Requests.MarkInterrupted] before admitting requests to lock
// in every pending spend line as unknown: the restart cut those calls off.
// Missing usage is unknown consumption, not zero cost. Spend lines contain
// attribution and usage, not prompts, completions or credentials.
//
// Callers establish authorization and workload identity. These storage operations
// neither reserve money nor enforce budgets. Inference owns admission limits and
// credential checks; telemetry owns operational measurements.
package usage
