# Janitor

The Janitor page shows work that needs you, each project's state and recent
Changes. Open a project for scans, findings, queues and Run now. Open a Change
for its files and evidence. Janitor settings holds the choices and repair limits. Nothing runs until a person saves choices.

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
for security fixes** and **Packs**. **Save** records your choices, name and time.
Security fixes need dependency updates; toolchain patches need security fixes.
Packs can stay on with dependencies off. The page also shows project choices and
where releases go automatically. A deployment's own page controls its release setting.
Changing the workflows keeps these choices in place. Every Change records what ran.
Turning choices off stops new claims; work that has already claimed permission may finish.

A project's own Janitor section controls its update level. Turning it off stops
both dependency and pack updates there. The choice records who changed it, when
and why; it appears with the choices as well as beside the project.

## When something goes wrong

Janitor settings reads repair limits from each project's adopted workflows. A failed
check starts the declared repair sessions. Work that still fails waits for a
person; a missing or unknown check never counts as success. If AppTown's check
cannot produce a verdict, it retries twice before holding the Change. Each
attempt's error remains recorded.

An empty update needs a final agent message with every part's release-listing
command and relevant output, and every base image's tag listing. A reference to
an uncommitted report file is insufficient.

Security fixes, dependency updates and pack updates have separate failure counts.
Each queue shows its workflow version, repair limit and stop rule once. Projects
are listed where they use different adopted versions. Current stopped queues
appear with the project's state. Stopping dependency updates leaves security
fixes able to run unless their own failure count has also stopped them.

A failed release, uncertain release progress or a rollback holds the project
back. A person's successful release of the newest code ends the hold. The table
lists deployments covered for automatic janitor releases, with their automatic
or window setting. A deployment's own page controls that setting. Protected
deployments still need a person's approval.

## Projects and history

Click a project row to open its Janitor section, with its recent tries and paths
to try again, chat or skip an update. The table shows the last scan, findings,
next dependency update and recorded level. Missing scans and old scans stay
visible; neither means the project is up to date. Held or stopped security fixes
appear in Needs you.

**Run now** starts the project's janitor queues straight away through the same
schedules and pack assessment used overnight, with the same choices. AppTown
records who asked. **Run now for all projects** starts each eligible project;
leaving the page leaves the remaining projects unstarted. Watch each queue on
its project row. **Try again** ends a stuck or stopped queue's failed tries so
it can try the same update again. **Close and try again** also closes the open
change shown, using your permission to close it. Waiting and updating queues
keep their progress and change links. A refused **Run now** can offer to close
the busy change and try again. History stays recorded. If the
new start fails, the next overnight run can still try. Agents use
`apptown janitor run -p PROJECT [-queue QUEUE] [-again]`.

**Last 7 days** lists landed Changes, changed dependency files or pack versions (older attempts retain package moves) and the
releases that contain them. Each Change links to its checks and approval.
**Older history** pages through earlier landings. The janitor's attribution names
the person who set the original choices and their revision, on the Janitor page and on the Change itself. Work without a
covering approval names its schedule instead. History does not replace the
current deployment or scan evidence.

Queue titles, schedules and repair limits come from each project's adopted
workflows. The Next update column and Run now action read the scheduler's current
answer. AppTown checks every candidate against the saved choices and requires
current authority at acceptance and landing.
