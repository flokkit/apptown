# Conformance

Which of our apps meet the ways of working they have adopted, and how to
bring one back when it does not.

## What you are looking at

Every project adopts some ways of working. Some of those carry **checks**: a
shell script, a maintained scanner, a probe of the running app, or a rubric a
model judges. The **Conformance** page answers one question for every
project: *does it meet what it adopted?* It has six tabs:

- **Projects**: one row per app, those needing attention first. Each row
  shows a finding count or a status, and one line saying what to do next
  ("Fix: *the check*", "Not known: *why*"), how many checks couldn't run and
  when it was checked. Apps that meet everything are folded under **Meet
  everything they adopted**.
- **Fleet reviews**: every review of more than one project, newest first,
  with what it found and how it moved since the one before ("2 fixed · 0
  new"). Reviews of one project are on that project's page.
- **Checks**: one row per adopted check with a tally of its results ("1
  finding · 2 passed · 1 not applicable"). Open one for each project's
  result and how the check judges: the actual script, rubric, scanner or
  probe, and where it runs.
- **Remediations**: jobs that fix findings across many projects (below).
- **Ways of working**: each pack, its recommended version, and which projects
  are on which version.
- **What-ifs**: recent trials of a pack version against the projects.

There is no percentage on purpose: a number would hide whether the missing
part failed or was never judged.

## A project's page

**View checks** on a row opens the project's page. It shows its past reviews
as bars (red for findings, amber for possible problems; select one to open
that review), a **Fixed** history (what got fixed, by which change, verified
by which review), and every check it adopted, grouped by result:

| Group | Meaning | What to do |
|---|---|---|
| Findings | the app does not meet the check | fix it, or record an exception |
| Couldn't run | the check was attempted but gave no answer, so its result is not known | read the reason, fix what the check needs, check again |
| Needs your review | a model's rubric flagged a possible problem | read the evidence; **Confirm finding** if it is real |
| Approved exceptions | it fails, but an exception accepts it on the record | nothing, until the exception expires |
| Not judged yet | no answer yet; each row says why ("waiting for a deployment", "not run yet") | wait for what it needs, or check again |
| Not applicable | the check applies to nothing this app declares, or judges one change and runs only at landing; each says why | nothing, unless the reason is wrong |
| Conforms | it passes | nothing |

Open a check to see its evidence: the commit judged, the check's output, and
any exception or confirmation behind the result.

**A possible problem is not a finding until a person confirms it.** The
confirmation covers that one result. After the next review the flag comes
back, showing who confirmed it before and at which commit, so confirming
again is a fresh decision.

## Checking

**Check now** checks every project's committed `main`, read-only: it opens no
change and touches no landing gate. The header shows its progress while it
runs. **Check this project** on a project's page does the same for one app.
AppTown also checks a project by itself when its adopted ways of working
change; that review is marked "adoption". A review that finished with checks
that could not run reads **incomplete**, in amber.

Every review is a saved record. Open one and the page shows exactly what it
recorded, at the commits it looked at, with the exceptions in force then,
under **As of review #N**. Nothing is recomputed and nothing can be changed
from there; **Back to current** returns to the present.

## Fixing something

On a project's page a finding has **Open a change to fix**. It opens an
ordinary change on that app whose acceptance test is *this check passing*.
The change goes through the app's usual gates and approval like any other.

An exception records a person's acceptance of a failing check, as an
accepted risk or a false positive, with a justification and optionally an
expiry date. It is recorded on the finding where the project's or a change's
findings list it (**Suppress…** or **FP…**). The check then reads
**excepted**, with the failure still visible underneath. When the exception
expires, the finding counts again without a new review.

## Fixing many at once: remediations

A **remediation** fixes findings across the estate, one change per project at
a time. Start one with **Fix everywhere** on a check, **Fix N problems** on a
project's page, or **Fix N problems** at the top of the page for every
finding.

Each item moves through: queued, working (an agent writes the fix),
**waiting on you** (accept it on the change page; it lands itself), landing,
verifying (a fresh review of that project), done. An item AppTown cannot
finish is **stuck** with a reason: **Retry** it with a note for the agent or
**Skip** it. A remediation can be paused, resumed or cancelled from its page.

The **Remediations** tab shows what is in progress and how many fixes run at
once (two by default; people allowed to configure AppTown can change it).
More at once costs more per hour and hits provider rate limits sooner; when a
provider refuses, work pauses for a few minutes and the item is queued again.

## When a pack changes

A project is judged against the version of each pack it adopted. A new pack
version moves no project until its owner adopts it. Before recommending a
version, run a what-if from its page (**Run on N eligible projects**): it
runs that version's checks on projects on an earlier version and shows what
would newly fail and what it could not assess, without changing anyone's
standing. Projects can adopt the version from the what-if's page. See
[Packs, versions and what-if](packs-and-what-if.md).

## When a check is judged

Under how a check judges, it says where it runs:

- **At landing and in every review**: it asks whether the tree as it stands
  has a property ("the Dockerfile runs collectstatic").
- **At landing only**: it asks whether one change did something acceptable
  ("this change did not add a networked database"), so a review shows it as
  not applicable. A model's rubric runs this way unless it declares
  `"scope": "tree"`; only checks judged in reviews can be tried in a what-if.
- **In every review, on the running deployment**: it needs the deployment's
  steady use ("each part uses under 90% of its memory limit"), which a
  preview does not have. No change can prove it before landing, so AppTown
  opens no remediation for it: make the change the finding describes and
  deploy, or override the deployment's resources on its card.

## Not applicable

Every check says what it applies to: the whole repository, or parts of the
app as `.apptown/app.yaml` declares them (every part built with Go, every
static site, every part built from a Dockerfile). AppTown decides before
anything runs. A check that applies to nothing here is **not applicable**,
with the reason ("no part is built with go"). It is never a pass and never an
error: it does not by itself stop a change landing, the change's record and
approval panel name it so it is not read as passed, and it never satisfies a
remediation. If the reason is wrong, declare the part's `tools:` in
`.apptown/app.yaml`.

A check that applies runs in the folder of each part it matches, and finding
nothing to judge there is a failure. The platform check "the contract matches
the code" catches code no part declares, so removing a tool from the
declaration does not make its check not applicable.

## From the command line

```
apptown review [-p <project>]      check every project, or one, and follow it
apptown whatif <pack> <version>    run a what-if on a pack version
apptown check                      run a change's checks on this folder; ○ marks one not applicable
```
