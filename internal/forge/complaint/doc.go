// Package complaint records reports about problems using Forge itself.
// Application findings and proposed code changes belong to their respective
// owners. Filing a complaint does not start work or change approval policy.
//
// # Filing and review
//
// HTTP, MCP and CLI adapters supply [Entry] text and the available project, Change,
// run, step, session and actor attribution. [Box.File] validates the text and
// records the report. The box trusts caller-supplied attribution; adapters own
// credential verification and authorization of intake and administration.
//
// A complaint says what it is about ([Entry].About): the forge (the default),
// the project's adopted way of working, whose packs the box names itself, or
// the project. A complaint filed in an eval's throwaway copy belongs to that
// eval, as the evals owner's copy record says; only those about the forge
// reach the forge's queue. Intake adapters derive the project from the
// filer's own credential where they can; a run cannot file into another
// project.
//
// [Box.All] returns the forge's queue, bounded, newest first, including archived reports.
// [Box.MarkSeen] marks all reports seen. [Box.Answer] records or replaces an answer;
// [Box.Archive] removes a report from the active inbox while retaining its record.
// Answer, archive and delete commit their mutations and audit receipts together.
// A receipt failure rolls back the mutation.
//
// # State and retention
//
// [StateOf] derives state from the answer and archive timestamp. Archived takes
// precedence, otherwise a nonempty answer means Answered, and no answer means New.
// Seen is independent of state. Answering an archived report does not reopen it;
// there is no unarchive operation.
//
// [Box.ArchiveOlder] archives unarchived reports before a caller-selected age.
// [Box.Delete] removes one report explicitly. Periodic maintenance calls
// [Box.Expire] to delete reports older than [Retention], regardless of state,
// with one audit receipt when a sweep deletes anything. No clustering or automatic
// repair is triggered by these operations.
package complaint
