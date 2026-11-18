# Janitor

Maintenance’s Needs you tab says how many projects need you or how many it keeps up to
date. When every choice is off, it says “Janitor is off”.
Open Settings › General for a project’s Janitor choice and Run now.
Open its record under Maintenance › Standards for scans and findings. Open a Change for its
files and evidence. Settings contains What to keep updated. The Janitor starts on:
patch and minor dependency updates, security fixes, base images, toolchain patches
for security fixes and packs. The choices show “Set by AppTown” until you change
them. Turn choices off in Maintenance settings, or turn the Janitor off for one
project in Settings › General.
[Who keeps what up to date](who-keeps-what-updated.md) says what the Janitor
leaves to people, the administrator and providers.

## What it may change

**Dependency updates** cover routine patch and minor updates, or aggressive updates
including major versions. Security fixes run on their adopted workflow’s schedule within
the same version limits. An agent updates every ecosystem, workspace and
sub-project it finds, following adopted dependency guidance. An AI model reads
the complete diff and judges its level and scope, including the smallest code
changes a moved package needs. When it cannot decide, the Change waits for a
person. With Base images on, the agent updates Dockerfile base images on the declared toolchain line. An image tag
and a numeric declaration agree when their shared version components match:
`node:24-slim` beside `node = "24.22.2"` stays on 24.22.2; Python 3.12 stays
on 3.12. A vendor version such as `java = "temurin-21.0.5+11.0.LTS"` declares
its major, so `eclipse-temurin:21-jre` stays on 21. The longest agreeing declaration wins. Images with no agreeing numeric
declaration keep the selected level’s scope. AppTown
checks that each external image keeps its name and tag variant. Stage references,
`scratch` and variable operands stay unchanged. Toolchains stay unchanged except
when Toolchain patches is on, for patch releases within the same minor that a named security finding needs. For a nonempty security fix, the reporting scanner must no longer report each
security target. Only the public part's running-image findings become image
targets. Static parts run AppTown's file server, so its image findings remain
scan observations rather than project fix targets; source dependency findings
still have their own fix targets. Findings in other parts need a person.
A finding whose scanner reports no fixed version is not a target; it stays on
the Security tab, and the first scan after a fix is published makes it one.
Image updates stay digest-pinned. The Janitor uses `apptown image newest` for
external FROM operands and `staticServer:`; AppTown checks routine moves against
the allowed line and refuses downgrades. Any real build on that line is allowed,
even when a newer build exists; a registry HEAD proves the candidate tag’s
digest. A refused lookup leaves that image unchanged.
A security target may choose the smallest pinned build that clears the finding.
Direct dependencies move to stable releases only. A package they pull in may be
a pre-release (alpha, beta, rc) when the stable release itself requires it; the
janitor never pins one.
**Keep packs current** moves projects to newly published pack versions and makes
any application changes those updates need. Repairs may update declarations in
`.apptown/app.yaml` required by the new pack's checks. Dependency updates and
security fixes change only an existing `staticServer:` in that file, when base
image updates are on; they never add a missing one.

In Maintenance settings, choose dependencies **Off**, **Patch and minor** or
**Including major**, plus **Security fixes**, **Base images**, **Toolchain patches
for security fixes** and **Packs**. Each switch or dependency choice saves immediately and records your name and
time. A concurrent change shows a conflict and the current choices; it does
not overwrite them.
Security fixes need dependency updates; toolchain patches need security fixes.
Packs can stay on with dependencies off. The page also shows project choices and
where releases go automatically. A deployment's own page controls its release setting.
Changing the workflows keeps these choices in place. Every Change records what ran.
Turning choices off stops new claims; work that has already claimed permission may finish.

Settings › General has a Janitor section. Its Dependencies choice saves when you
change it. Choose Same as everywhere to follow the shared choice, or Off, Patch
and minor, or Including major for this project. Off stops both dependency and
pack updates there. The choice records who changed it and when. Maintenance settings
names projects with their own choice and links to their settings.

## Fresh images for security fixes

The stock policy is that a running image has no OS-package vulnerability for
which a fixed package is published. The image scan judges that outcome.
With Security fixes on, the Janitor also requests a rebuild when a running
scan reports a finding absent from that image digest's usable build-time scan.
It applies recorded exceptions and the scan's severity threshold first.
A missing or erroring build-time scan leaves the baseline unknown, so the
Janitor can request a rebuild. A running scan error triggers no rebuild. While the latest scans could not be
assessed, the project’s scans schedule retries at the earlier of its next cron
time and one hour after its last firing. Findings and stale successful scans
keep the ordinary cron.

A rebuild releases the running release's same commit, taking current packages
through the Dockerfile's final-stage upgrade. It applies only to deployments
following main with a recorded running image and no release in progress.
Ordinary release approvals, holds and windows still apply. Manual and promotion
feeds receive no automatic rebuild. A project's own security workflow can
leave out the `rebuild` step.

A scan with no project fix targets closes as an observation after the rebuild
passes or is not applicable. Its findings remain recorded. A failed rebuild
keeps the Change open.

A finding already reported at build does not trigger another rebuild: the
recipe could not clear it. This includes fixes needed inside a compiled binary
or a Dockerfile missing its upgrade. Source and base-image fixes still use the
agent's normal fix workflow. If a rebuilt image's bundle scan cannot judge it,
while the next running scan works, the unknown baseline can cause another
rebuild each such day. These are ordinary recorded releases. Repetition stops
when a usable bundle scan reports the remaining finding.

Two builds of the same commit can contain different packages. The released
image digest records what ran. Rollback reuses that release's stored image;
only a missing image requires rebuilding it.

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

An empty update needs a final agent message with release-listing commands and
relevant output for parts that declare dependencies, plus the resolver answers
and refusals for external base images and `staticServer:`. A stale answer names
its reading time and registry error.
`scratch`, stage names, variable operands and parts without a manifest or
Dockerfile need no listing. Write the account in the final message, not a report
file. When an independent check passes the account, the empty Change closes as
nothing to change without asking a person. This includes a security fix when
no release or newer image build within the level fixes the finding. The scan's
findings and verdict stay recorded, candidate scans are not applicable, and the
next scheduled scan can offer the same targets again.

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
window in its Settings tab. Automatically and When a person says save on change.
Choose In a release window, then set the window and press Save release window.
Protected deployments still need a person's approval. Unknown scans appear in
Attention and the project record under Maintenance › Standards. A project without Janitor queues is neither listed nor counted here. An unreadable update
schedule on a project with queues offers **Run now** and reports why it cannot start.

## Running updates and history

**Run now** in Settings › General’s Janitor section starts its queues through
the same schedules and pack assessment used overnight, with the same choices. AppTown
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
