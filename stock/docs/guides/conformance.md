# Conformance

Which of our apps meet the ways of working they have adopted, and how to
bring one back when it does not.

## What you are looking at

Every project adopts some ways of working (under **Ways of working**). Some
of those carry **checks**: a shell script, a maintained scanner, a probe
against the running app, or a rubric a model judges. The **Conformance**
page answers one question for every project: *does it meet what it adopted?*

The page has three tabs, in the order people need them:

- **Projects** — one row per app, worst first. Each row has a plain sentence
  ("Does not conform · 2 findings (1 high)", "Conforms so far · 4 of 8
  judged", "Conforms · 8 of 8") and a bar showing its checks by lane. There
  is no percentage on purpose: a number would hide whether the missing part
  failed or was never judged.
- **Reviews** — every review ever run, newest first, with what it found and
  how it moved since the previous one ("2 fixed · 0 new"). A review is a
  saved record. Open one and the whole page — projects, their details, the
  checks — shows exactly what that review recorded, at the commits it
  looked at, with the exceptions that were in force then. Nothing is
  recomputed. A person starts one with **Review now** or `forge review`;
  the forge starts one for a project by itself whenever that project's
  adopted ways of working change (adopting a pack or a version, a guidance
  edit), shown as "adoption" beside the review.
- **Checks** — every adopted check: what it requires in plain words, how it
  is judged (the actual script, rubric or tool), *where* it is judged, and
  each project's verdict. Guidance authors work from here.

## The lanes

A project's checks sort into lanes, and the lanes decide its standing:

| Lane | Meaning | What to do |
|---|---|---|
| Findings | the app does not meet the check | fix it, or record an exception |
| Check errored | the evaluator broke, not the app | look at the error; the app is not judged |
| Flagged by AI review | a rubric judged this, no person has yet | read the evidence; confirm it, or record it as a false positive |
| Excepted | it fails, but an exception accepts it on the record | nothing, until the exception expires |
| Not yet judged | no verdict yet, or the app moved since the last one | run a review |
| Not applicable in a review | a check about what one *change* did; it runs only when a change lands | nothing |
| Conforms | it passes | nothing |

**AI flags do not count as findings until a person confirms them.** A
confirmation belongs to that one verdict. After the next review the flag
comes back fresh, showing who confirmed it before, so the second click is
informed rather than automatic.

## Running a review

**Run a review now** judges every project's committed `main`, read-only. It
opens no change and touches no landing gate. While it runs you see each
verdict arrive; when it finishes the page refreshes to the new picture. A
review that needs an app image will reuse the one the standing deployment
already runs for that exact commit; failing that it builds a few, and says
so when it could not.

**Re-review this project** on a project's page does the same for one app.

## Fixing something

On a project's page, a finding has **Open a Change to fix**. It opens an
ordinary change on that app whose acceptance test is *this check passing*.
Nothing lands by itself: the change goes through the app's usual gates and
approval like any other.

An exception (a disposition with a justification) is recorded from the
project's Security tab on the finding itself. The next review shows the
check as **excepted**, with the failure still visible underneath. When the
exception expires, the finding shows again without a new review.

## When a pack changes

A project is judged against the version of each pack it adopted. Saving a
pack makes a new version; recommending one changes what new projects get
and what others are offered, and no project moves until its owner adopts
it. Before recommending, **What-if** on the pack's page runs that version's
checks on every project on an earlier version (or `forge whatif <pack>
<version>`) and shows, project by project, what would newly fail and what
it could not assess, without changing anyone's standing. A project that
then adopts the version is reviewed against it automatically. See Packs in
[Running a forge](/docs/guides/running-a-forge).

## Two kinds of check

A **state check** asks whether the tree, as it stands, has a property:
"the Dockerfile runs collectstatic", "the README has no marketing tone". It
is judged when a change lands *and* in every review.

A **transition check** asks whether one change did something acceptable:
"this change did not add a networked database". It only makes sense with a
before and an after, so it is judged when a change lands and shows as *not
applicable* in a review. Each check says which it is under **How it's
judged**. A model rubric is a transition check unless it declares
`"scope": "tree"`; only a state check is judged by a review or a what-if,
so a rubric meant to be tried across the estate before a version is
recommended declares the tree scope.

A few runtime checks are judged **only on the running deployment**: "each
part uses under 90% of its memory limit" needs the deployment's steady use,
which a change's fresh preview does not have. They are skipped at landing
and judged in every review. No change can prove one before it lands, so such
a finding is not remediation work: the forge refuses to open a remediation
for it and skips a queued item with the reason. Fix it with an ordinary
change the finding describes (declare more memory) and deploy, or override
the deployment's resources on its card.

## Fixing many at once: remediations

A **remediation** is a job that fixes findings across the estate, one Change
per project at a time. Start one from a check ("Fix everywhere"), a project
("Fix all on this project"), or the page header ("Fix all findings"). The
**Remediations** tab shows each job's progress and the **remediation
workers**: how many fixes the forge works on at once (two by default; change
it on the tab). A bigger pool costs more per hour and hits provider rate
limits sooner; when a provider refuses, the pool holds off for a few minutes
and the item goes back in the queue by itself.

Each item walks: queued, working (an agent on the implement lane), **waiting
on you** (accept it on the change page; it lands itself), landing, verifying
(a fresh review of that project), done. An item the forge cannot finish is
**stuck** with a reason, and you can retry it with a note or skip it with a
reason. A project's page keeps a **Fixed** history: what got fixed, by which
change, proven by which review.
