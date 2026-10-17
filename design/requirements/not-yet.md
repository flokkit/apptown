# Scenarios we are thinking about that the forge does not support today

> Product intent and journeys, recorded at the dates below. Their past “live”
> and “today” wording is not a fresh deployment audit. Use the
> [current product model](../subsystems/product-model.md) and its topic guides
> for checked implementation; preserve requirement gaps rather than silently
> redefining promises to match code.

Status: living list, started 2026-09-06: use cases and scenarios under
consideration that are not supported today.
Companion to `requirements.md`, which describes only what is built. This
document holds the journeys people will try and the forge cannot yet
carry: what happens today if you try, what the journey should be, and
what it would take. It is written in the same use-case form so an entry
can move into `requirements.md` when it is built, with its number.

Rules for the list: an entry is a **scenario** a person or an app would
face, not a feature idea (the maintainers keep those in their work index).
Each says what happens today, honestly. An entry that gets built is moved,
not deleted. Entries are grouped like the requirements so the gap sits
next to the journeys it extends.

## Deployments and clouds (extends B Deployments and releases)

**N1. Upgrade a managed database's major version.** An owner bumps
`postgres: { version: "16" }` in the contract of an app whose cloud
deployment runs a 15.
*Today:* previews boot a fresh 16 with throwaway data, fine. The cloud
reconcile finds a cluster that does not match what was asked and fails
the deploy as terminal; nothing is upgraded. Tearing the deployment down
keeps the old cluster as retained; a new deployment creates an empty 16.
*Should be:* a gated effect. Freeze the app's writes (a contract field the
forge can ask for, not assume), fork the cluster at the provider, run the
provider's in-place major upgrade on the fork, verify with the boot gate
against the new binding, swap the binding as the next generation,
unfreeze; the old cluster stays in the retained list until a person
releases it. Dump-and-restore for small databases; logical replication
only for databases that cannot pause.
*It would take:* a freeze/maintenance signal in the contract; fork and
upgrade verbs on the provider driver; a binding swap that is its own
generation; the retained-resources ledger as it is.

**N2. Move an app between targets.** An owner wants the same deployment
on a different target (App Platform to Cloud Run, or the intranet to a
cloud).
*Today:* create a new deployment on the new target, deploy, tear the old
one down; the app's data does not come along, and there is no window in
which both serve.
*Should be:* create beside, migrate the needs' data (N1's mechanics),
switch the address, retain the old.

**N3. Restore a deployment's data from a backup.** *Built for Docker
hosts, 2026-09-08 (B31, B32):* daily and post-release sets of the data
volume and each need, restore with a safety set first. *Still not:*
cloud targets, where the managed database's own backups are the only
copy — the provider driver should list them for a need instance and
restore one as a new generation with a receipt; and a retention policy
per deployment rather than the fixed seven.

**N4. Attach AWS or Google Cloud.** Designed (`design/subsystems/deployment.md`
§15), not built; DigitalOcean is the only cloud provider with a driver.

## Integrations (extends B Services and secrets)

**N5. Offboard a service across the fleet.** An integration administrator
retires a connector every product uses.
*Today:* one product at a time — detach, run the retire lane, repeat; the
connector cannot be removed until the last reference is gone.
*Should be:* one act opens the retire change on every project that uses
the connector, worked by the remediation queue one project at a time,
with the connector's removal gated on the last one landing.

**N6. Deprovision what a connector created.** Detaching a linked service
leaves the provider-side project, key or DSN in place.
*Today:* by design; detach never deletes. The clean-up is a person's job
at the provider.
*Should be:* a separate, confirmed verb offered for linked integrations
only, recorded, never automatic.

## Ways of working (extends B The project's way of working)

**N7. Adopt an update across many projects at once.** A standards
administrator wants every project on `go-small` v2.
*Today:* each owner prepares and adopts; the fleet view shows who is
where. (`ways-of-working-versions.md` §12.)
*Should be:* a batch that prepares every project, shows the consequences
per project, and opens uplift changes where code must move.

**N8. Approve a weakening as an expiring exception.** Dropping a gating
block takes the standards authority today, but the act is a receipt, not
an exception with an expiry and an approver distinct from the requester.

**N9. A pack with an owner.** A security team's pack should be editable
by that team only; today anyone with the manage-packs capability may edit
any pack.

**N10. Cut a version of a custom pack on save.** Only stock packs get
versions cut when their pins change; an institution pack edited in the
UI has no version history a project can be pinned to.

**N11. A forge upgrade that changes the platform contract.** A new forge
binary needs something of every app (a health endpoint, a new contract
field).
*Today:* the platform pack cuts a new version and every project shows an
update available; nothing says which binary versions a pack version is
compatible with, and a project that never adopts keeps running until it
does not.
*Should be:* a compatibility declaration between the `forge` pack's
versions and the binary, and forced contract changes opened as batch
adoption changes (N7) by the forge itself.

**N12. Re-validate an approval given before the rules moved.** A landing
approved, then the project adopted a new revision before the merge: the
gates re-run under the new revision; the human approval does not. Absorbed
into N22.

## Changes and landing (extends C Changes and landing)

**N13. A change across two projects.** One feature that touches two
repositories.
*Today:* two changes, landed separately, nothing ties them.
*Should be:* at least a link between changes and a landing that refuses
to ship one without the other, or a stated rule that the forge does not
do this.

**N14. Keep the baseline honest across a rebase.** A change reconciled
with a moved `main` loses the provenance of which findings were already
there; the gate may blame the change for what `main` brought.

**N15. Compare two attempts at the same change.** The founding fan-out,
compare and select (`state-model.md` §4) — three models, three designs,
pick one with the comparison as evidence. Not built.

## People and authority (extends A People and authority)

**N16. Sign in with an outside identity provider.** Internal accounts and
a trusted-header proxy exist; there is no OIDC of the forge's own.

**N17. Transfer a project to another owner.** The route exists; no page
or CLI verb reaches it.

## Seeing what happened (extends C the record)

**N18. A saved report over the ledger.** "Every landing this quarter and
who approved it" as a saved query with a receipt of its own, for an
auditor. Conformance reviews are the only saved reports today.

**N19. Reconcile history someone made outside the forge.** A repository
imported with years of commits, or a mirror someone pushed to directly:
nothing turns unattested commits into records.

## The forge's own duties (extends D)

**N20. Upgrade the forge across an era.** The era-upgrade test is stranded
on a SQLite-era snapshot (`design/architecture/testing-map.md` §5); the
promise "current binary, yesterday's data" is kept by the manual
stopped-service procedure, not by a test.

**N21. Restore the forge itself from a backup.** Nightly dumps exist; a
rehearsed restore of the whole data directory (database, hubs,
workspaces, secrets bootstrap) onto a fresh machine does not.

## Proposed defaults from the business-analysis review (2026-09-06)

An independent review of `requirements.md` proposed seven defaults. They
are recorded here with what is true today; none is built, and three need
an owner decision before they are requirements. The review's stance,
which the forge shares: put the defaults in the requirements and keep
stricter rules configurable through ways of working.

**N22. Acceptance is specific and carried forward.** Acceptance means "I
am happy for this proposed change to land, provided its checks pass", of
a recorded tree.
*Today:* acceptance is one act; it counts for the landing's approval where
the accepter could approve that step, was shown its sentence and the checks
raised nothing new; the catch-up with main and any rework after acceptance run under
the same approval with nothing shown; the accepted commit is not
recorded. (Absorbs N12.)
*Should be:* record the accepted commit; a clean catch-up proceeds under
the acceptance after the checks re-run; when the tree the person
accepted changes, show what changed and ask again; changes to automated
checks trigger rechecking, changes to what a person is asked to
authorise need fresh acceptance. Self-acceptance stays the default; a
lane may require another person. *Ruling needed:* every change, or only
where the lane says so.

**N23. Service configuration travels with the change that needs it.**
*Today:* attaching a service makes its contributions reach previews and
standing deployments at once, and detaching stops them at once,
regardless of whether the wiring change has landed.
*Should be:* a newly attached service is available to the change's
preview and becomes active in each standing deployment when the code
that uses it is deployed there; swapping and retiring keep the current
arrangement working until the replacement is ready; abandoning the change
leaves the running application alone. A configuration-only change is
lightweight but shows what will change and uses the deployment's normal
deployment authority.

**N24. Tell the truth about partial success; keep rollback modest.**
*Today:* when the deploy fails after the merge, the landing run blocks on
the step with the change still in landing; the page offers step-level
doors and does not say the change is on main and the previous version is
still running; abandoning that landing sends a merged change back to in
progress. Rollback is deploying an earlier bundle with no data
compatibility check.
*Should be:* three separate facts on the page — your change is on main;
deployment failed and the previous version is still running; retry the
deployment or open a fix — with no trip back through acceptance to retry
the same accepted artifact. Rollback described as reverting application
code with a compatibility check for its data; when compatibility is
unknown, say so and offer a deliberate recovery choice, initially
"restore through your provider, then record the outcome here" (N3).

**N25. Exceptions have one understandable set of rules.** One human
explanation — "we know this check failed, and we are choosing to
proceed" — with the finding or failed check, a reason, who decided, and
the scope.
*Today:* three mechanisms (scanner suppressions, conformance exceptions
with a second approver by recommended default, step overrides); a failed
scanner records as an error rather than a finding but blocks the gate the
same way, so the only exit is an override.
*Should be:* the one explanation over the three mechanisms; a failed
scanner shown as "we could not check"; operational controls such as
permission checks never bypassable through "proceed anyway". *Ruling
needed:* the review's defaults — a maintainer accepts ordinary technical
debt alone; temporary deferrals carry a revisit date; false positives stay
dismissed until the finding changes; projects require a second approver
for selected categories — reverse the current conformance default of
recommended dual control.

**N26. Archive by default; delete deliberately.**
*Today:* delete is the only door (B5); it refuses while a run is in
flight and takes the records with it.
*Should be:* "we are finished with this project" archives it — stops new
work, retains history, accounts for anything still running or costing
money. Permanent deletion stays available: show the records and
resources affected, offer an export, keep a small deletion receipt in the
instance audit history. The promise becomes "history survives normal
retirement", not "everything is kept forever".

**N27. The helper drafts a small definition of success.**
*Today:* the living narrative summarises what was done, after the fact;
uplift and remediation changes carry acceptance criteria; an ordinary
change has only its request.
*Should be:* for an ordinary change the helper turns the request into a
few concrete outcomes and assumptions, visible on the change before work
proceeds; small changes proceed without a separate approval of that text;
significant ambiguity prompts a question first; at acceptance each
outcome shows what was checked and what still needs a person's judgment;
an automated regression test is captured where useful. The cheapest of
the seven, and the natural partner of N22.

**N28. Operational commitments are explicit and proportionate.**
*Today:* no project setting distinguishes an experiment from an
application people rely on; there are no spending limits and nothing
measures a budget; the nearest thing is the per-deployment audience,
data class and protected flag.
*Should be:* one project setting with concrete consequences: experiments
get spending limits, preview cleanup and clearly disposable data;
relied-upon applications additionally need a named maintainer, a stated
backup and recovery arrangement, and deliberate handling of destructive
changes, initially manual. Budget exhaustion pauses new agent work and
never switches off a running application. *Ruling needed:* one switch,
or grown from the deployment attributes.

## Found while remediating the fleet (2026-09-06)

**N29. A skipped ship step does not resume when the step before it is
re-run.** Quick Poll's landing: the image scan failed for a scanner
reason, the deploy and the release record were skipped; after the scan
was re-run and passed, each skipped step had to be re-run by hand, in
order.
*Should be:* re-running a failed step re-dispatches the steps that were
skipped because of it, in order, so "retry the deployment" is one act
(the review's N24).

**N30. The change page says "waiting on you" before the gate has run.**
The state is derived from the run's steps and the pending approval wins
over a running boot or gate, so a person is called before there is
anything to judge.
*Should be:* "waiting on you" only when every step before the approval
is terminal.

**N31. A stock lane fix reaches a project only when it adopts.** *Partly
closed 2026-09-07: every project's lanes were moved to the current stock
in one pass through the adoption door (32 projects, records only, no
landing — an update that carries no new checks needs none). What remains
is the standing rule that makes this happen without a person running a
script: a stock lane correction that every project should take.* The
implement lane's self-verify prompt was corrected on 2026-09-06; every
project pinned at the older library version kept the old prompt, by
design (a release never moves a project). Skills resolve to latest and
carried the rule, but a step's own prompt wins over a skill.
*Should be:* the batch adoption of N7, or a way for the forge to declare a
stock change as a correction that every project should take. Until then,
remediations run on the quick-change lane, whose one step carries the
request and its scope. The same holds for a check's rubric: the Django
admin check was tightened to version 8 on 2026-09-06 (the password must
actually be supplied) and the four Django projects sit on legacy
snapshots pinned at version 7 with the django-htmx pack unplaced, so the
fleet review keeps judging them by the old rubric until someone re-adopts
each one.

**N32. The landing's test step has no baseline.** greeting-api's
integration test needs a Docker daemon (Testcontainers); the landing's
test step has none, so the tests fail on main as they did before the
change, and a README fix was wedged on it. Guidance checks and scans are
judged against the change's base; the project's own tests are not.
*Should be:* a test failure that reproduces identically on the base
commit is the estate's debt (reported, a remediation opened) and not the
change's; and a project whose tests need Docker declares it, so the step
gets the private daemon the implement lane already has.
*Built 2026-09-07 (first half):* on a failure the step runs the same
command over the change's base commit; if it fails there too the step
passes with "carried: the same tests fail on the base commit", the
receipt names the commit. A passing suite costs nothing extra. Not yet:
the Docker daemon for suites that need one, and a remediation opened for
the carried debt.

**N33. The build-artifacts check misses tracked build directories.**
`repo/no-build-artifacts` looks for compiled binaries over 2 MB by magic
bytes. greeting-api tracks `target/` (Maven output, mostly text reports)
on main; every test run rewrites it and the engine commits the churn.
*Should be:* the check flags tracked files under build-output directories
(target, dist, build, node_modules, coverage, __pycache__, .venv) so a
remediation untracks and ignores them once, and the scope guard stops
being the last line.
*Built 2026-09-08, differently:* no list of folder names — which folders
are build output depends on the language. The platform check
`no-ignored-files-tracked` asks git which committed files the tree's own
`.gitignore` covers (the check image now carries git); each lane's block
requires its ignore list to name what its tools write
(`gitignore-names-build-output`); `no-build-artifacts-tracked` refuses any
file over 2 MB that is not data or media. The scope guard asks the same of
a change's diff on every change, not only remediations, and the forge's
own template no longer whitelists the retired `.forge/compose.yaml`.

**N34. A remediation cannot add a package it needs without a fight.**
Closed the same day: the scope guard refused any dependency-file change,
so an app's admin fix configured whitenoise and then restored
`requirements.txt`, and boot failed. Now a dependency file
that only gained lines is in scope; bumps and removals are still refused.
Kept here because the additive rule is a heuristic over diffs: a lockfile
that reorders itself when a package is added will still read as moved.
*Should be:* the guard compares dependency sets by ecosystem, not lines.

**N35. `forge config validate` judged less than boot does.**
Closed the same day for `secretspec.toml` (validate now runs boot's
parser, and the parser refuses tables the forge never reads, naming
them). Still true for the rest of what boot checks — declared readiness
paths, ports, init containers against the image — so an agent's "forge
config validate passed" is weaker than "it booted".
*Should be:* one validation the CLI, the gate and boot all call, so a
change that validates also boots.

**N36. `forge check` judges less than the gate does.** It runs the shell
and tool checks; the model-judged rubrics and the runtime probes run only
at the gate and in the fleet review. So an agent's "forge check passes,
including the admin check" is not true of the rubric checks, and the
first time a rubric judges the change is after the agent has handed it
back (letterbox change 240: the reviewer noticed the named checks were
absent from the output, 2026-09-06). The brief now carries the rubric,
which helps the agent aim; it does not let it verify.
*Should be:* `forge check` runs the rubric checks too (they cost a model
call each, so opt-in or on demand), so the agent sees the gate's answer
before the gate.

**N37. The Go tools run a fixed Go, not the project's declared toolchain.**
`go-vet` and `gofmt` run in `golang:1.25-alpine` whatever `mise.toml`
says; reading-list declares Go 1.26.7, and a go.mod that names it cannot
be built by the gate's tool (change 245, 2026-09-06). The toolchain
block asks the image to match mise; the forge's own tools do not.
*Should be:* a tool that runs the project's language runs the version
the project declares — resolve the image tag from mise.toml, or run the
tool through mise in the workspace.
*Built 2026-09-08, for Go:* the Go bindings (`gofmt`, `go-vet`,
`go-mod-tidy`) run with `GOTOOLCHAIN=auto`, so the module's own `go`
directive picks the toolchain and Go downloads it into the shared module
cache; four projects on Go 1.26/1.27 had read "requires go >= 1.26.7
(running go 1.25.14)". Other languages' tools still run a fixed image.

**N38. An error becomes a change, and the change closes the error.** The
forge attaches apps (and itself) to GlitchTip and its Services card shows
each unresolved issue with where it broke (2026-09-07). What is missing is
the loop around it:
1. the issue as a sentence on the project page under Happening now ("2
   new errors since release #10, first seen 00:33, in Conformance.tsx:703";
   polling first, a GlitchTip alert-rule webhook later);
2. a "Fix this" door that opens a change on the `fix-bug` lane with a
   brief the forge writes — title, where, the latest event's stack and
   breadcrumbs, first-seen release, count — whose first job is a failing
   test that reproduces it;
3. resolve-on-landing: the landing marks the issue resolved in that
   release and the change page says which issue it closed;
4. comebacks: the same issue after that release reads "came back in
   release #12, fixed by change 251" and opens the door again;
5. ignore and resolve doors on the card, recorded with who and why, so
   the forge's record matches the tracker's.
*Order:* the sentence and the resolve door; then Fix-this with the brief;
then resolve-on-landing and comebacks together; the webhook last.

**N39. The review's scope guard stayed silent once.** greeting-api change
250's first round carried four files under `target/` (two `.class`, two
maven-status lists) with a report that said `target/` had been restored;
the deterministic guard that fails a change for build output and for
unclaimed files recorded nothing, and the model passed it (2026-09-07).
The same guard fired correctly on recipe-book 238. The receipt now records
what the guard saw (`scopeSaw`: base and files) on every review, so the
next occurrence is diagnosable; the cause is still unknown.
*Should be:* the guard's silence is impossible to mistake for its
approval, and build output fails a change on every lane, not only where a
remediation target is set.

**N40. Environment variables the app requires, derived from a declaration.**
`deploy/env-declared` (built and withdrawn the same day, 2026-09-08)
inferred requirements from source with per-language regexes and a list
of names the forge provides; it would have broken the first new app. It
was guesswork of exactly the kind the checks are meant not to be. *Should
be:* the language's own tooling names the reads (a Go analysis pass, a
Django settings introspection, Spring's property metadata), the contract
says what the forge provides, and the check is a set difference between
two declarations — or nothing.

**N41. Withdrawn the same day (2026-09-08), with the reason.** Alongside
N40: `docs/build-md` checked that BUILD.md *mentions* the test command,
not that documented commands work; `stable-week` counted the forge's own
health-controller restarts and blamed the app for a forge incident;
`errors-quiet` read a triage state (unresolved issues) as code quality;
`unused-dependencies` needed an ever-growing ignore list of build tools
and a disabled rule to stay quiet, a list nobody could keep up. What
survives is derived from a declaration or a maintained tool with no
list to tend: ignored files, hadolint, lockfile agreement, non-root,
plus two short lists kept for now by an owner decision (the data-file
extensions of the large-file check, the ignore-list spellings).
