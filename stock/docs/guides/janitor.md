# Janitor

The Janitor page says how many projects need you or how many it keeps up to
date. When no choices are set, or every choice is off, it says “Janitor is off”.
Open a project for scans, findings, queues and Run now. Open a Change for its
files and evidence. Settings contains What to keep updated. Nothing runs until
a person saves choices.

## What it may change

**Dependency updates** cover routine patch and minor updates, or aggressive updates
including major versions. Security fixes run on their adopted workflow’s schedule within
the same version limits. An agent updates every ecosystem, workspace and
sub-project it finds, following adopted dependency guidance. An AI model reads
the complete diff and judges its level and scope, including the smallest code
changes a moved package needs. When it cannot decide, the Change waits for a
person. With Base images on, the agent updates Dockerfile base images within the selected level. AppTown
checks that each external image keeps its name and tag variant. Stage references,
`scratch` and variable operands stay unchanged. Toolchains stay unchanged except
when Toolchain patches is on, for patch releases within the same minor that a named security finding needs. The reporting scanner must no longer report each security target. Only
the public part's running-image findings become image targets; other parts need
a person.
Direct dependencies move to stable releases only. A package they pull in may be
a pre-release (alpha, beta, rc) when the stable release itself requires it; the
janitor never pins one.
**Keep packs current** moves projects to newly published pack versions and makes
any application changes those updates need. Repairs may update declarations in
`.apptown/app.yaml` required by the new pack's checks. Dependency updates and
security fixes keep that file unchanged.

In Janitor settings, choose dependencies **Off**, **Patch and minor** or
**Including major**, plus **Security fixes**, **Base images**, **Toolchain patches
for security fixes** and **Packs**. Each switch or dependency choice saves immediately and records your name and
time. A concurrent change shows a conflict and the current choices; it does
not overwrite them.
Security fixes need dependency updates; toolchain patches need security fixes.
Packs can stay on with dependencies off. The page also shows project choices and
where releases go automatically. A deployment's own page controls its release setting.
Changing the workflows keeps these choices in place. Every Change records what ran.
Turning choices off stops new claims; work that has already claimed permission may finish.

A project's own Janitor section controls its update level. Turning it off stops
both dependency and pack updates there. The choice records who changed it, when
and why; it appears with the choices as well as beside the project.

## When something goes wrong

A failed check starts the adopted workflow's declared repair sessions. Work that
still fails waits for a person; a missing or unknown check never counts as success. If AppTown's check
cannot produce a verdict, it retries twice before holding the Change. Each
attempt's error remains recorded. AppTown failures retry on the next scheduled
run, including an identical update. After three failed tries the queue stops.
A failed check that judges the update still waits for a person and suppresses
an identical proposal. Overriding a check keeps the Change visible while its
remaining checks and acceptance finish. If a later check fails or cannot finish,
the Change waits for your decision. A stopped security queue can start a
new finding, but does not retry a finding that exhausted its tries until a
person asks it to try again. Janitor's update and repair agents have 45 minutes
per step.

An empty update needs a final agent message with every part's release-listing
command and relevant output, and every base image's tag listing. A reference to
an uncommitted report file is insufficient.

Security fixes, dependency updates and pack updates have separate failure counts.
Stopping dependency updates leaves security fixes able to run unless their own
failure count has also stopped them.

Needs you shows the project, what it was doing and why it needs you. A decision
says what the change would do and why it stopped, then offers **Accept** or
**Close**. Pack changes name the ways of working they move; source changes name
the changed files. A recorded empty source proposal says “Makes no change”.
The reason describes the current check or says “Waiting for your acceptance”. Accepting a failed
check asks for your reason and records an exception; its result stays on the
record. The remaining checks run, then the Change asks for acceptance. Close
skips the update and keeps the same proposal from coming back.

AppTown failures stay out of Needs you while they retry. After three failed
tries, the row says “AppTown could not finish this update” and offers **Try
again**. Technical details stay on the Change page.

A failed release, uncertain release progress or a rollback holds the project
back. Its row opens the deployment. A person's successful release of the newest
code ends the hold. A deployment's own page controls its release setting and
window. Protected deployments still need a person's approval. Unknown scans appear in Attention and the project's scan history. A project
without Janitor queues is neither listed nor counted here. An unreadable update
schedule on a project with queues offers **Run now** and reports why it cannot start.

## Running updates and history

**Run now** in a project's Janitor section starts its queues through the same
schedules and pack assessment used overnight, with the same choices. AppTown
records who asked. **Try again** ends the named queue's failed tries and closes
the open automated Change shown, using your permission to close it. It can try
the same update again. History stays recorded. If the new start fails, the next
overnight run can still try. Agents use
`apptown janitor run -p PROJECT [-queue QUEUE] [-again]`.

**Recent updates** lists landed Changes from the last seven days: project, what
changed and when. Dependency rows show package moves; pack rows name only ways
of working whose version changed or that were added. A first adoption names all
its packs. Click a row to open its Change for checks, acceptance and releases.
**Older history** pages through earlier landings. History does not replace
current deployment or scan evidence.

Queue titles, schedules and repair limits come from each project's adopted
workflows. Run now reads the scheduler's current answer. AppTown checks every
candidate against the saved choices and requires current authority at acceptance
and landing.
