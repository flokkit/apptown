# Conformance records and reviews

Conformance owns reviews, verdicts, confirmations, exceptions and current-status
projections. Verification executes checks; the Check library defines evaluator
results and provenance. The [quality guide](quality.md) describes the full flow.

A review someone asks for is admitted with an authorized selection of projects
and saved as `queued` with its request (projects and build choice), in the same
transaction as the job that runs it ([background jobs](jobs.md)). The job names
only the review's ID; the one delivery that claims it (`queued` to `running`)
runs the saved request. Remediation's check of one item still admits and runs a
review directly. A recorded change of a project's adoption (not its
creation) admits a queued review of that project in the adoption's own
transaction, trigger "adoption", under a scope the adoption owner builds
for that revision (`conformance.WithAdoption`): it may admit only that
project's standing review while the revision is current, never a what-if
(`RunWhatIf` is its own action), a read or a decision. A failed admission
fails the adoption change. History clearing is refused while any review is queued or
running. Its execution object
binds recorded verdicts to the admitted project, declaration version and source.
Callers cannot substitute another project or check after evaluation. Terminal
review state rejects further writes.

Reviews assess adopted declarations against committed source. Publishing a new
block does not change earlier verdicts or silently replace a project's adoption.
Current conformance is a projection of recorded evidence, with source/version
identity retained. Failure to evaluate is distinct from a negative judgment.

## Pack versions and what-if

Each pack has one recommended version ([versioned ways of working](ways-of-working-versions.md)).
Conformance judges the version a project adopted, never the recommended one:
moving the recommendation, or publishing a stricter version, changes no project's
standing until the project adopts it through Adoption.

A **what-if** answers "what would happen to these projects on this pack version?"
before anyone adopts it, for projects on the pack or not yet on it (they would
join). It needs the same authority as a fleet review over the selected projects.
A request that names no projects runs on every eligible one, read after that
authority and stored as the list it was: an active ordinary repository-backed
project that directly holds the pack below that version
(`adoption.ProjectsBelowIn`).
For each project, Verification prepares the candidate adoption without saving it
(`adoption.Service.PicksWith`, then `Prepare`), judges the version's checks and
the project's adopted ones on the same committed `main`, and compares them by
block and check name: would fail, would pass, unchanged, no longer required, or
not assessed. Shell and tool checks run on the tree, an image tool building
within the fleet review's build budget; a runtime probe reads the standing
deployment only when it serves exactly that commit; model rubrics run only when
the starter includes them with a spend limit, asked before each project's
checklist and stopped by any spend that is unknown (overshoot: one project's
checklist, at most two calls; spend is "at least" when unknown and is its own
`what-if` line in the usage ledger). Every row not judged names one reason from
a fixed set (`conformance.NotAssessed`) with the tool's own detail. Both sides keep
the exception matcher's answer, as adoption would: a whole-check exception names
its block version and stops covering a moved block; an identifier exception (a
scanner rule) still covers it. The read answers per project
(`conformance.WhatIfByProject`), worst first: new failures (including excepted
today, failing under the version), could not assess (something the version
changes was not judged; one evaluation both sides share is not part of the
change), no new failures, not evaluated. Beside the outcome each project has
a score that splits its checks by the version's side (judged — passing or
failing — could not assess, no longer required), its unjudged checks grouped
by reason with what would make them assessable (`NotAssessed.Remedy`), and
the rules the version moves: each is a row of its own (`added`,
`newer-version`, `older-version`, `removed`; no check), never folded into the
outcome. The what-if's fleet summary groups the unjudged checks by check and
reason across projects, so a check that can never be judged stands out.
Recent what-ifs are listed under Conformance. Adopting from a what-if is each project's own adopt door via a Change,
naming the what-if (`fromWhatIf`): AppTown reads from it the pack, version,
adoption revision and candidate it assessed for the project, refuses a
what-if not `done` or with no candidate for the project, and refuses a
candidate that is no longer the one assessed. The Change's brief names the
what-if, the commit and the project's outcome; that line is context a person
can edit with the brief, and the binding is the recorded target digest. A
recommendation can name the finished what-if of its version it was decided
on, which its `pack.recommended` receipt keeps. While a what-if is queued or
running, a project it has not reached is "not yet judged", and "not
evaluated" only once it has ended. Rows go to the what-if's own two tables,
which nothing that decides standing, gates, exceptions or remediation reads. A
what-if begins no review and writes no verdict. A what-if is queued and claimed
like a review. One stopped by shutdown or its two-hour budget, or found queued
or running when AppTown restarts, is settled as interrupted; it is not
resumed.

An **eval** of a pack version
re-runs real Changes in throwaway copies under that same candidate and compares
each arm's landed work with the Change's base over both arms' checks, finding by
finding, with the landing's provenance rule (`check.Classify`) in both
directions: new, fixed, already there, or not comparable when the check did not
answer on both trees. It uses the same assessment as a what-if.

## Boundaries

A review does not itself create a fix or bypass an approval. Remediation is a
separate application operation. There are no dimension-assurance or override
records.

## Implementation references

- [internal/forge/conformance/service.go](../../internal/forge/conformance/service.go)
- [internal/forge/conformance/execution.go](../../internal/forge/conformance/execution.go)
- [internal/forge/conformance/service_test.go](../../internal/forge/conformance/service_test.go)
- [internal/forge/verification/fleet.go](../../internal/forge/verification/fleet.go)
- [internal/forge/conformance/what_if.go](../../internal/forge/conformance/what_if.go)
- [internal/forge/verification/what_if.go](../../internal/forge/verification/what_if.go)
