# Scenarios AppTown does not support

Companion to `requirements.md`, which describes only what is built. This
document holds the journeys people will try and AppTown cannot yet carry:
what happens if you try, what the journey should be, and what it would take.
It uses the same use-case form, so an entry can move into `requirements.md`
when it is built.

An entry is a **scenario** a person or an app would face, not a feature idea.
Each says honestly what happens now. When one is built it moves to
`requirements.md`. Entries are grouped like the requirements so the gap sits
next to the journeys it extends.

## Deployments and clouds (extends B Deployments and releases)

**N1. Upgrade a managed database's major version.** An owner bumps
`postgres: { version: "16" }` in the contract of an app whose cloud
deployment runs a 15.
*Now:* previews boot a fresh 16 with throwaway data, fine. The cloud
reconcile finds a cluster that does not match what was asked and fails
the deploy as terminal; nothing is upgraded. Tearing the deployment down
keeps the old cluster as retained; a new deployment creates an empty 16.
*Should be:* a gated effect. Freeze the app's writes (a contract field the
AppTown can ask for, not assume), fork the cluster at the provider, run the
provider's in-place major upgrade on the fork, verify with the boot gate
against the new binding, swap the binding as the next generation,
unfreeze; the old cluster stays in the retained list until a person
releases it. Dump-and-restore for small databases; logical replication
only for databases that cannot pause.
*It would take:* a freeze/maintenance signal in the contract; fork and
upgrade verbs on the provider driver; a binding swap that is its own
generation; the retained-resources ledger as it is.

**N2. Move an app between targets.** An owner wants the same deployment
on a different target (the intranet to a cloud, say).
*Now:* create a new deployment on the new target from a copy of the old
one's data, deploy, and take the old one down; there is no window in which
both serve the same address.
*Should be:* create beside, move the data, switch the address, retain the
old.

**N3. Restore a cloud deployment's data in place.** On a Docker host a
backup set restores in place, with a safety set first. On a cloud target a
copy can start a new deployment or a restored copy, but not restore over the
standing one; and retention is not set per deployment.

**N4. Attach AWS or Google Cloud.** DigitalOcean is the only cloud provider
with a driver.

## Integrations (extends B Services and secrets)

**N5. Offboard a service across the fleet.** An integration administrator
retires a connector every product uses.
*Now:* one product at a time: detach, run the retire workflow, repeat; the
connector cannot be removed until the last reference is gone.
*Should be:* one act opens the retire change on every project that uses
the connector, worked by the remediation queue one project at a time,
with the connector's removal gated on the last one landing.

**N6. Deprovision what a connector created.** Detaching a linked service
leaves the provider-side project, key or DSN in place.
*Now:* by design; detach never deletes. The clean-up is a person's job
at the provider.
*Should be:* a separate, confirmed verb offered for linked integrations
only, recorded, never automatic.

## Ways of working (extends B The project's way of working)

**N7. Adopt an update across many projects at once.** A standards
administrator wants every project on `go-small` v2, or a correction to a
stock workflow's prompt or a check's rubric to reach every project.
*Now:* each owner prepares and adopts; the fleet view shows who is where. A
release never moves a project, so a project pinned at an older version keeps
the older prompt or rubric until someone re-adopts it.
*Should be:* a batch that prepares every project, shows the consequences
per project, and opens uplift changes where code must move; and a way for
AppTown to mark a stock change as a correction every project should take.

**N8. Approve a weakening as an expiring exception.** Dropping a gating
block takes the standards authority, but the act is a receipt, not an
exception with an expiry and an approver distinct from the requester.

**N9. A pack with an owner.** A security team's pack should be editable
by that team only; anyone with the manage-packs capability may edit any
pack.

**N10. An AppTown upgrade that changes the platform contract.** A new AppTown
binary needs something of every app (a health endpoint, a new contract
field).
*Now:* the platform pack cuts a new version and every project shows an
update available; nothing says which binary versions a pack version is
compatible with, and a project that never adopts keeps running until it
does not.
*Should be:* a compatibility declaration between the `apptown` pack's
versions and the binary, and forced contract changes opened as batch
adoption changes (N7) by AppTown itself.

**N11. More policy rule types and enforcement modes.** Three rule types
exist (`requireStepBefore`, `requireCapabilityBound`, `scanFailOnCeiling`).
*Should be:* rule types added as needed, each a core change
(`requireWorkflowExists`, `requireGuidanceImported` to bridge policy and
guidance, `requireApprovalBy`, `allowedModelProviders`, `releaseRequires`,
`timeoutCeiling`); each type stating where it applies (save, run preflight,
release preflight, configuration change); each adoption choosing warn or
enforce, for a warn-observe-enforce rollout; and adoption across the instance
beside per project.

## Changes and landing (extends C Changes and landing)

**N12. A change across two projects.** One feature that touches two
repositories.
*Now:* two changes, landed separately, nothing ties them.
*Should be:* at least a link between changes and a landing that refuses
to ship one without the other, or a stated rule that AppTown does not
do this.

**N13. Keep the baseline honest across a rebase.** A change reconciled
with a moved `main` loses the provenance of which findings were already
there; the gate may blame the change for what `main` brought.

**N14. Compare two attempts at the same change.** Three models, three
designs, pick one with the comparison as evidence. There is no fan-out,
compare or select (`state-model.md` §4).

**N15. Seed data for previews.** The contract parser accepts an app part's
`seed:` string, but nothing runs it; declaring it does not populate a preview.
*Should be:* after needs are ready and migrations finish, an app-owned seed
command runs before a preview is announced ready, with fixtures in the
repository at the same revision as the schema and fake data only. It runs for
previews and explicitly non-production deployments, never production, bound to
the deployment's tier rather than a flag. A pack could supply the fixture and
command, tests could check known records, and a preview could offer "Reset test
data". Seeding is not migration: schema setup may be needed in production,
sample data stays disposable.

## People and authority (extends A People and authority)

**N16. Sign in with an outside identity provider.** Internal accounts and
a trusted-header proxy exist; there is no OIDC of AppTown's own.

**N17. Transfer a project to another owner.** The route exists; no page
or CLI verb reaches it.

## Seeing what happened (extends C the record)

**N18. A saved report over the ledger.** "Every landing this quarter and
who approved it" as a saved query with a receipt of its own, for an
auditor. Conformance reviews are the only saved reports.

**N19. Reconcile history someone made outside AppTown.** A repository
imported with years of commits, or a mirror someone pushed to directly:
nothing turns unattested commits into records.

## Proposed defaults

An independent review of `requirements.md` proposed seven defaults. They
are recorded here with what is true now; none is built, and three need an
owner decision before they are requirements. The stance AppTown shares:
put the defaults in the requirements and keep stricter rules configurable
through ways of working.

**N20. Acceptance is specific and carried forward.** Acceptance means "I
am happy for this proposed change to land, provided its checks pass", of
a recorded tree.
*Now:* acceptance records the commit it accepted, and counts for the
landing's approval only when the landing merges exactly that commit, the
accepter could approve that step and was shown its sentence and what the
change adds ([work landing acceptance](../architecture/work-landing-acceptance.md)).
Any other candidate, including one produced by a clean catch-up with main or
by work swept in at admission, needs a fresh approval; nothing shows the
person what changed since they accepted. An approval given before the project
adopted a new revision is not asked again, though the gates re-run.
*Should be:* a clean catch-up proceeds under the acceptance after the checks
re-run; when the tree the person accepted changes otherwise, show what changed
and ask again; changes to automated checks trigger rechecking, changes to what
a person is asked to authorise need fresh acceptance. Self-acceptance stays
the default; a workflow may require another person. *Ruling needed:* every
change, or only where the workflow says so.

**N21. Service configuration travels with the change that needs it.**
*Now:* attaching a service makes its contributions reach previews and
standing deployments at once, and detaching stops them at once,
regardless of whether the wiring change has landed.
*Should be:* a newly attached service is available to the change's
preview and becomes active in each standing deployment when the code
that uses it is deployed there; swapping and retiring keep the current
arrangement working until the replacement is ready; abandoning the change
leaves the running application alone. A configuration-only change is
lightweight but shows what will change and uses the deployment's normal
deployment authority.

**N22. Tell the truth about partial success; keep rollback modest.**
*Now:* when the deploy fails after the merge, the landing run blocks on
the step with the change still in landing; the page offers step-level
doors and does not say the change is on main and the previous version is
still running; abandoning that landing sends a merged change back to in
progress. Rollback is deploying an earlier bundle with no data
compatibility check. Re-running a failed step does not re-dispatch the
steps skipped because of it, so each has to be re-run by hand, in order.
*Should be:* three separate facts on the page: your change is on main;
deployment failed and the previous version is still running; retry the
deployment (one act, which re-runs what was skipped) or open a fix, with no
trip back through acceptance to retry the same accepted artifact. Rollback
described as reverting application code with a compatibility check for its
data; when compatibility is unknown, say so and offer a deliberate recovery
choice.

**N23. Exceptions have one understandable set of rules.** One human
explanation ("we know this check failed, and we are choosing to
proceed") with the finding or failed check, a reason, who decided, and
the scope.
*Now:* three mechanisms (scanner suppressions, conformance exceptions
with a second approver by recommended default, step overrides); a failed
scanner records as an error rather than a finding but blocks the gate the
same way, so the only exit is an override.
*Should be:* the one explanation over the three mechanisms; a failed
scanner shown as "we could not check"; operational controls such as
permission checks never bypassable through "proceed anyway". *Ruling
needed:* the review's defaults (a maintainer accepts ordinary technical
debt alone; temporary deferrals carry a revisit date; false positives stay
dismissed until the finding changes; projects require a second approver
for selected categories) reverse the conformance default of recommended
dual control.

**N24. Archive by default; delete deliberately.**
*Now:* deletion is soft and restorable for a while, then purged; there is
no archived state that stops new work while keeping the project indefinitely.
*Should be:* "we are finished with this project" archives it: stops new
work, retains history, accounts for anything still running or costing
money. Permanent deletion stays available: show the records and
resources affected, offer an export, keep a small deletion receipt in the
instance audit history.

**N25. The helper drafts a small definition of success.**
*Now:* the living narrative summarises what was done, after the fact;
uplift and remediation changes carry acceptance criteria; an ordinary
change has only its request.
*Should be:* for an ordinary change the helper turns the request into a
few concrete outcomes and assumptions, visible on the change before work
proceeds; small changes proceed without a separate approval of that text;
significant ambiguity prompts a question first; at acceptance each
outcome shows what was checked and what still needs a person's judgment;
an automated regression test is captured where useful. The cheapest of
the seven, and the natural partner of N20.

**N26. Operational commitments are explicit and proportionate.**
*Now:* no project setting distinguishes an experiment from an
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

## Checks and the gate

**N27. The change page says "waiting on you" before the gate has run.**
The state is derived from the run's steps and the pending approval wins
over a running boot or gate, so a person is called before there is
anything to judge.
*Should be:* "waiting on you" only when every step before the approval
is terminal.

**N28. A project's tests that need Docker.** When a change's tests fail,
the step runs them again on the base commit; if they fail there too the
step passes as carried debt, naming the commit. A suite that needs a
Docker daemon (Testcontainers, say) cannot run in the tests step, and the
carried debt opens no remediation.
*Should be:* a project whose tests need Docker declares it, so the step
gets a private daemon; and carried debt is reported with a remediation
opened.

**N29. The scope guard reads dependency files by line.** A dependency file
that only gained lines is in scope for a remediation; bumps and removals are
refused. A lockfile that reorders itself when a package is added reads as
moved.
*Should be:* the guard compares dependency sets by ecosystem, not lines.

**N30. `apptown config validate` judges less than boot does.** It runs boot's
parser, including `secretspec.toml`, but not the rest of what boot checks:
declared readiness paths, ports, init containers against the image. An
agent's "apptown config validate passed" is weaker than "it booted".
*Should be:* one validation the CLI, the gate and boot all call.

**N31. `apptown check` judges less than the gate does.** It runs the shell
and tool checks; the model-judged rubrics and the runtime probes run only
at the gate and in the fleet review. So an agent cannot verify a rubric
check before it hands the change back; the brief carries the rubric, which
helps it aim but not verify.
*Should be:* `apptown check` runs the rubric checks too (they cost a model
call each, so opt-in or on demand).

**N32. Tools for languages other than Go run a fixed version.** The Go
bindings (`gofmt`, `go-vet`, `go-mod-tidy`) run with `GOTOOLCHAIN=auto`, so
the module's `go` directive picks the toolchain. Other languages' tools run a
fixed image whatever `mise.toml` declares.
*Should be:* a tool that runs the project's language runs the version the
project declares.

**N33. The review's scope guard can be silent.** The deterministic guard that
fails a change for build output and unclaimed files has recorded nothing on a
change that carried build output, and the model passed it. Each review's
receipt records what the guard saw (`scopeSaw`: base and files), so a
recurrence is diagnosable; the cause is unknown.
*Should be:* the guard's silence is impossible to mistake for its approval,
and build output fails a change on every workflow, not only where a
remediation target is set.

**N34. Environment variables the app requires, derived from a declaration.**
Inferring requirements from source with per-language patterns would break the
first new app, and is the guesswork checks must not be.
*Should be:* the language's own tooling names the reads (a Go analysis pass,
a Django settings introspection, Spring's property metadata), the contract
says what AppTown provides, and the check is a set difference between two
declarations, or nothing. A check ships only when it is derived from a
declaration or a maintained tool, with no hand-kept list to tend.

## Errors

**N35. An error becomes a change, and the change closes the error.** The
AppTown attaches apps (and itself) to GlitchTip, and the Services card shows
each unresolved issue with where it broke. What is missing is the loop
around it:
1. the issue as a sentence on the project page under Happening now ("2
   new errors since release #10, first seen 00:33, in Conformance.tsx:703";
   polling first, a GlitchTip alert-rule webhook later);
2. a "Fix this" door that opens a change on the `fix-bug` workflow with a
   brief AppTown writes (title, where, the latest event's stack and
   breadcrumbs, first-seen release, count) whose first job is a failing
   test that reproduces it;
3. resolve-on-landing: the landing marks the issue resolved in that
   release and the change page says which issue it closed;
4. comebacks: the same issue after that release reads "came back in
   release #12, fixed by change 251" and opens the door again;
5. ignore and resolve doors on the card, recorded with who and why, so
   AppTown's record matches the tracker's.
*Order:* the sentence and the resolve door; then Fix-this with the brief;
then resolve-on-landing and comebacks together; the webhook last.
