# SCM operations

`Service` owns durable merge effects and repository serialization. `GitDrivers`
builds a driver per operation, which prepares the Git/provider effect, executes
it, or inspects its recorded proof. Credentials are read once per driver.
It receives immutable `Source` facts; it does not read Workflow tables or decide
whether a change is permitted to land.

Workflow guards and admits the merge first. After SCM reports its result,
`workcommands.LandingEffects` records drift, snapshots the resulting contract and
publishes the branch where appropriate. SCM records the merge result and its Workflow receipt in one transaction;
Workflow advancement follows separately.

Git cancellation, exact operation trailers and credential freezing are private
SCM tests. Root integrations cover admission before Git, transaction rollback,
provider results, explicit inspection and settlement.

`PrepareClosure` takes the same repository lock before a caller's SQL transaction.
Its `Closure.CheckIn` interprets the durable intent for the caller's run set;
pending and recorded merges both refuse abandonment. Workflow owns membership
of that set and checks historical runs as well as active ones. Provider calls
without a recorded intent remain outside the local-merge guarantee.

`ReadCommittedDiff` compares resolved commits against a recorded starting base.
It reports an empty net tree only after a successful comparison; a missing branch,
missing base or failed Git command is an error. It does not inspect dirty files.

`ChangeBase` is where "what this change changes" starts: the merge-base of the
branch tip and the default branch in the hub (three-dot semantics), so a change
that caught up with main does not show main's files as its own. For a change
already landed it is the merge-base with main before the landing merge. The
recorded base stands in when the hub has no default branch or no common history,
and for a change fast-forwarded onto main, which no merge records. Every reader of a change's own diff or files (the change page, the
approval brief, the narrative, the reviewer's diff and scope, the acceptance
scope, the MCP diff) starts there; `ReadChangeDiff` is `ReadCommittedDiff` from it.

## Where main stands

`project_main` records, per project, the commit main stood at when AppTown
last moved it, the door that moved it (`landing`, `init`, `seed`, `copy`,
`genesis`, `integrity-adopt`, `backfill`), the landing run, and when. Absence
is unknown. A database snapshot therefore names a commit that exists and is
main or an ancestor of it, unless main was rewritten outside AppTown.

Git moves main first, under the repository lock; the door reads main back
(`PrepareMainMove`, before its transaction) and records it in the transaction
that settles the move (`MainMove.RecordIn`). The record only goes forward: a
main that does not descend from it, or cannot be read, is not recorded, and an
unchanged main keeps the door that moved it there. The compare-and-set against
the record read at preparation fails the caller's whole settlement
(`ErrMainChanged`). The participant never takes the lock; every caller holds it
(`Service.Merge`, `Inspect`, lifecycle initialization, seed, copy and genesis,
integrity adoption).

A landing's settlement records `changes.landed_sha` (the merge result) and the
main read back; they differ when recovery settles an older merge after another
door moved main on. A local merge writes its intent before Git runs, so a failed
settlement is found by startup inspection. A merge-request result and an
already-merged branch are observed without a prior intent: a failed observation
asks for a retry, and the landing's retry settles it; until then the older
record stays.

`ReadMainStanding` compares the record with the hub for the project page: equal
says nothing; otherwise "main is N commits ahead of AppTown's record",
"the recorded … is no longer in main's history", "AppTown has no record of
where main stands" or "could not read main". It says what differs, never who
moved main. With an `origin`, it also compares the record with the last fetched
`refs/remotes/origin/<branch>` (ahead and behind counts, without a network
call; a missing tracking ref is unknown, not equal). A project created before
AppTown recorded main has no record; `apptownd -backfill-recorded-main`, run
once while no apptownd serves the database, records each such project's current
main as where recording began (a backfill, not a witnessed landing).

## Branch publication

`Branches.Publish` is the only way a change's branch moves in a project's hub.
It checks two things together, under the repository lock:

- **Who may move it.** `branch_authority` records a generation and holder per
  branch. A branch nobody was handed is generation 1, held by `initial`; the
  first take makes it 2. The holder is the change's one worker: a chat, a
  writing run or a desktop ticket, taken only once the previous holder has
  ended and never handed over (`workflow.TakeChangeIn`; a release, such as a
  ticket's sync, records `released`). A landing holds nothing. When nobody
  holds the change, a landing's admission, a desktop checkout and the
  services editor take it for a moment under a name of their own
  (`workflow.TakeRequestIn`), held only while this process runs them. A step
  attempt is admitted to publish only while its root run holds the branch
  (`ClaimAuthorityIn`); a retry advances the generation but never takes the
  branch back from another holder. A publisher presents the generation its
  attempt was admitted under (`Step.SourceGeneration`), or acts for the
  current holder (a chat's commits, a ticket's push). An earlier generation
  is refused even when its commit would be a clean fast-forward. A grant
  may carry its own check, asked inside the admission: a desktop push is
  refused once its ticket has expired or been revoked.
- **What it brings in.** Every commit a publication adds (reachable from its
  tip, and neither from the branch as published nor from the default branch,
  the hub's `HEAD`), against each of its parents: nothing under `.apptown` but
  the native contract, no file over 10 MiB, and 50 MiB of new files in all.
  The budget is per publication, over the distinct new file contents
  (blobs): each version of a file counts, identical content counts once.
  History brought from another branch is checked like new work, and
  permission to rewind never skips the check of a move that adds commits.
  A refused publication moves nothing (`ErrNotPublished`).
- **One ordering point.** Publication admission, taking the change and
  attempt admission all create the branch's authority row if it is missing and lock
  it, so none of them can interleave with another, including the first.
- **What it moves from.** The branch moves only forward from the value this
  operation read (or the tip the publisher says it started from), with
  `git update-ref` checking the old value. A workflow's restart may rewind to
  an ancestor, never sideways.

A publication is recorded in `branch_publications` before git runs and deleted
once settled. `AdvanceAuthorityIn` refuses while one is recorded, so a new holder
never overtakes a publication already admitted. Git runs as a prepared
`update-ref --stdin` transaction: it holds the branch's lock file from prepare
to commit, and aborts on its own if AppTown dies before sending commit.

A workflow `restart` retry resets the workspace to the run's base and rewinds
the branch with a guarded rewind under the new attempt's generation, so the
replacement's commits build on what is published.

`Recover` runs at startup, from `RecoverLandings`. For each recorded
publication: if the branch's
`.lock` file exists, a git process may still finish, so it stays unknown; if
the branch is at the proposed commit it is settled as published; if at the
expected commit, as abandoned. An unknown publication holds its branch (no
new holder, no publication). To resolve one, confirm no git process is working
in that repository, remove the stale `refs/heads/<branch>.lock`, then restart
or retry: the next publication or startup settles it by reading the branch.

A desktop push with a ticket lands in `refs/apptown/incoming/ticket-<id>`. The
hub's pre-receive hook refuses early a push from a moved tip or one that does
not build on it; AppTown then publishes the holding ref if the ticket still
holds the branch, and removes it either way.

`MergeMain` makes, in the hub, AppTown's own merge of `main` into a
change's branch: an ordinary merge commit (the branch, then `main`, by the
forge), only when the branch is behind `main` and the merge has no
conflicts; otherwise it names the conflicting files and makes nothing. It
moves no ref. The caller publishes the merge through `Branches.Publish`
like any other commit, so who may move the branch is decided there
(`repositoryedit.PublishMainMerge`, before an agent step on a worker).

A change's folder is used by one holder at a time. A chat or a writing run
that takes it commits what the previous worker left before working there,
so a stale writer's files are never published as the next holder's work
([workflow](../workflow/README.md#who-works-where)).

## Checked landing

A landing merges one pinned commit (the plan's step 1b). Every new
repository-backed root landing records `landing.pin.required`; its gates and
merge are refused without a candidate and never fall back to the live
workspace. `workflow` records `landing.candidate` run events: the
change-branch commit (candidate) every source gate checks, and the
default-branch commit (target) the branch contains. It is pinned at
admission, after the change's committed work is published under its
holder's grant (the chat's, or a momentary request's when nobody holds it;
uncommitted files are not included, and are named in a `landing.uncommitted`
event), and again each time the reconcile step runs. The reconcile step only
reads: a branch that does not contain the default branch is refused ("behind
main"). A recheck publishes the chat's new commits the same way before it
reruns anything, and reruns the reconcile step whenever it reruns anything.
A workflow a landing calls checks the landing's candidate.

A new candidate resets, anywhere in the run, every result that named the old
one: source gates (tests, scans, guidance conformance, scripts), overrides of
them, and approvals that were decided or had votes. Each gate names the
candidate in every outcome, including rework. Scripts in a checked landing run
on a staged copy of the candidate and commit nothing. Guidance conformance
stages the candidate from the project's repository, reads its diff and its own
`.gitignore` files, and does not probe a preview it cannot prove runs the
candidate (runtime checks are then not judged). Every vote records the
candidate it was cast on. An acceptance on the working run records the
commit it was given on (the change's branch as committed then; uncommitted
files are not part of it), and carries to the landing only when the landing
merges exactly that commit: work published after the acceptance needs a
person's fresh approval.

Merge admission refuses while any passed gate, override or approval names
another commit or none; while a passed agent step or workflow call (neither
can be bound to a candidate) is present; while a gate in a stage the run has
left was reset by a new candidate (nothing reruns an earlier stage, so the
landing is restarted); or while an approval in a stage the run has left is
undecided. With a candidate pinned, `Service.Merge` publishes nothing: the
local merge proceeds only while the branch is at the candidate, the default
branch at the target, and the candidate contains the target, so the merged
tree is the checked one (`LandingMoved` otherwise). If the default branch
moved, restart the landing.

A landing whose run records a GitLab merge request merges through it only
when GitLab guarantees that what merges is exactly the candidate. Read at
the moment of merging: the project's merge method must be fast-forward
(`ff`) or semi-linear (`rebase_merge`), both of which refuse a branch not
up to date with its target; GitLab must report that it does not rebase
automatically before merging (`automatic_rebase_enabled` false; a project
that does not say is refused); squashing must be off for the project
(`squash_option` not `always`) and the request; the local branches must be
pinned as for a local merge; and GitLab's default branch must still be the
target. The candidate is pushed as that exact commit (never forced), the
request must report it as its head, and the merge names it (`sha`,
`squash: false`). Anything else is refused before GitLab is asked to merge
(`ErrMergeRequestSettings`, naming the setting, or `LandingMoved`). After
the merge (or when the request was already merged), what GitLab merged must
be exactly the checked landing: the candidate itself, or one merge commit
whose parents are the target and the candidate, with the candidate's tree,
and not squashed. Only then does AppTown's default branch move, to that
commit, never to later GitLab history. A setting changed on GitLab between
the read and the merge can still land something else there; AppTown
cannot prevent that race on the provider, but it does not record that merge
as landed, and its main does not follow.

## What GitLab is told

A landing's `Source` carries an `Attribution`: who opened the change, its
title and body, and what admitted the merge (each approving vote of the
deciding round with its step's sentence and declared approvers, the landing
authority recorded when the run was admitted, overrides with their reasons,
and the candidate checks with the result each recorded). Workflow reads it
in the admission transaction and again at each check-in, and refuses the
attempt (`ErrChanged`) when it differs, so it names what that check-in
admitted. The check-in's transaction ends before the Git and GitLab calls;
a withdrawal after it is the same window every merge already has.

Every landing's merge message adds `AppTown-Change-Id`, `AppTown-Opened-By`,
`AppTown-Approved-By` (account, step, time and sentence; no role),
`AppTown-Landing-Authority`, `AppTown-Override` and `AppTown-Change-Page` to
`AppTown-Run-Id` and `AppTown-Workflow`; a local merge still ends with
`AppTown-Operation`. A GitLab fast-forward makes no merge commit, so there the
description is the only GitLab-side record. Before `MergeMR`, `executeMR`
replaces the merge request's whole description with
`MergeRequestDescription` (`workflowexec` writes the opening one with the
same function). If GitLab refuses that write, nothing merges and the attempt
is retried as a whole. A request already merged on GitLab is settled without
a write: it was not AppTown's merge. The declared approvers are a
declaration, not the authority a vote was checked against (a root passes
without matching them; an empty list means the project's defaults then), and
are labelled so.

On GitLab, an account name appears in a code span, so it never links to a
GitLab user of that name; a name containing `@` reads "a Forge account (name
withheld)", in the declared approvers too (a group so named reads "a Forge
group (name withheld)"). Text AppTown did not write appears only as code
(an indented block or a code span), and no line AppTown writes starts with
`/`, so GitLab runs no quick action and renders no mention or HTML. GitLab
also scans raw descriptions and commit messages, code included, for closing
statements ("fixes #12", "closes <issue URL>") and closes those issues when
the merge reaches the default branch. So in all such text, descriptions and
trailers alike, `#`, `!` and `@` are separated from what follows and a link's
scheme from its address (`https:// …`); AppTown's own change-page link is
left whole. An email address written into a change's body or a reason is
published as written; only account names are withheld.

## Scratch refs

`ScratchRef` names the one ref a step attempt's runtime token may push
(`refs/apptown/scratch/change-<change>/step-<step>-a<attempt>`), so that
`apptown check` can check an agent's commit on a worker before the step's work
is collected. The git endpoint and the hub's pre-receive hook admit that push
and no other for the token; a scratch commit moves no branch and is checked as
feedback only. `SweepScratch` removes the refs of attempts no longer live,
given the caller's reading of liveness; one it cannot read is kept.
