# Janitor

Maintenance › Needs you says how many projects need you or how many it keeps up to
date. When every choice is off, it says “Janitor is off”.
Open Settings › General for a project’s Janitor choice and Run now.
Open its record under Maintenance › Standards for scans and findings. Open a Change for its
files and evidence. Settings contains What to keep updated. The Janitor starts on:
patch and minor dependency updates, security fixes, base images, toolchain patches
and packs. The choices show “Set by AppTown” until you change
them. Turn choices off in Maintenance settings, or turn the Janitor off for one
project in Settings › General.
[Who keeps what up to date](who-keeps-what-updated.md) says what the Janitor
leaves to people, the administrator and providers.

## What it may change

**Release lines.** A language, runtime or package that
[endoflife.date](https://endoflife.date) tracks moves by its release line, the
cycle endoflife.date lists: PHP 8.3, Python 3.12, Node 24, Go 1.26, Postgres
16, Django 5.2. Patch and minor keeps each on its line (PHP 8.3.35 → 8.3.36,
never 8.3 → 8.5). Moving to another line is a major move: only at Including
major, when it is due (below). A package endoflife.date does not track keeps
its semver major as its line. This applies to Dockerfile base images,
`staticServer:` and the direct dependencies in the lockfiles AppTown reads.
When AppTown cannot read the line (endoflife.date is unreachable, or it does
not track an image, as with `php`), it takes the narrowest line, the version's
x.y, and the Change says the line is unknown: a patch still moves, nothing
else does, at either level. Because endoflife.date lists no Docker image for
PHP, a php image never moves to another line by itself. The administrator can
point AppTown at a mirror of endoflife.date's data (`-endoflife-url`, see
[Setting up a server](server-setup.md)); nothing else in AppTown reads it.

**Dependency updates** cover patch and minor updates (**Patch and minor**), or
those plus major versions when they are due (**Including major**). Security fixes run on their adopted workflow’s schedule within
the same version limits. An agent updates every ecosystem, workspace and
sub-project it finds, following adopted dependency guidance. An AI model reads
the complete diff and judges its level and scope, including the smallest code
changes a moved package needs. When it cannot decide, the Change waits for a
person. With Base images on, the agent updates Dockerfile base images on the declared toolchain line. An image tag
and a numeric declaration agree when their shared version components match:
`node:24-slim` beside `node = "24.22.2"` stays on 24.22, or on 24.22.2 with
Toolchain patches off; Python 3.12 stays on 3.12. A vendor version such as `java = "temurin-21.0.5+11.0.LTS"` declares
its major, so `eclipse-temurin:21-jre` stays on 21. The longest agreeing declaration wins, whatever the level: Including major never
moves a declared toolchain's image to another major, because that changes the
toolchain, which needs a person. An image with no agreeing numeric
declaration stays on its release line at Patch and minor; at Including major it
moves to the newest line only when that is due. AppTown
checks that each external image keeps its name and tag variant. Stage references,
`scratch` and variable operands stay unchanged. Toolchains stay unchanged except
when Toolchain patches is on: then a toolchain may move to a patch release within
the same minor, in every declaration together (mise or version files, go.mod's
toolchain line and the build image). For a nonempty security fix, the reporting scanner must no longer report each
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
janitor never pins one. For npm, uv, Go module and Bundler projects AppTown
checks this itself: the stable-direct steps ask the package manager for the
project's direct dependencies, read from the committed lockfile, and fail an
update that makes one a pre-release. A direct pre-release already on main is
shown on the step but does not hold the update. For other package managers the
update's model check judges it.
**Including major** keeps each direct dependency and base image on its current
line and moves it to the newest line only when one of these holds:

1. Its current line ends support within 3 months, or has ended, by
   endoflife.date (Django 5.1 ended while 5.2 is supported: 5.1 is due to move,
   to the newest line). The registry deprecating the newest release of the
   line (npm) counts too.
2. One of AppTown's current source scans has a finding in it that is fixed only
   on a newer major.
3. The newest line has settled: its first release is at least 6 months old and
   at least 2 releases have followed it, by the registry's publish dates. For a
   base image this is unknown, since endoflife.date does not count releases, so
   an image moves line only by rule 1.

Otherwise the package gets patch and minor updates, as at Patch and minor; a
0.x package's minor counts as its major. The agent asks `apptown janitor majors`
which moves are due. AppTown decides again from the change's own lockfiles
(npm `package-lock.json`, `uv.lock`, `go.mod`, Bundler `Gemfile.lock` and
`composer.lock`) and fails the update when a direct dependency moves to a line
that is not due, naming why; the repair agent then keeps it on its major. The
change's routine step lists each major move with the rule that allowed it, for
example "django 4 → 5 due: 4.2 ends support 2026-04-30 (endoflife.date)". A fact
AppTown cannot read (a registry or endoflife.date unreachable, a product
endoflife.date does not track) allows nothing and is listed as unknown. An
indirect package may change major when a moved direct dependency requires it.
With any other package manager, direct dependencies stay on their majors.
Rule 2 is for packages; base images follow rules 1 and 3.

**Keep packs current** moves projects to newly published pack versions and makes
any application changes those updates need. Repairs may update declarations in
`.apptown/app.yaml` required by the new pack's checks. Dependency updates and
security fixes change only an existing `staticServer:` in that file, when base
image updates are on; they never add one. A project without one is served with
the newest `caddy:2-alpine` build at each build.

In Maintenance settings, choose dependencies **Off**, **Patch and minor** or
**Including major**, plus **Security fixes**, **Base images**, **Toolchain patches**
and **Packs**. Each switch or dependency choice saves immediately and records your name and
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

The agent updates everything it can within the level. Its final message is a
note shown with the Change, not evidence: when it leaves something that could
move, or a finding unfixed, it says what and why; otherwise it needs to say
nothing. AppTown never refuses a Change over that note. An empty Change closes
as nothing to change without asking a person once AppTown's own tests,
start-up and scans pass on the unchanged code. This includes a security fix when
no release or newer image build within the level fixes the finding. Anything
left that is not a security finding gets another attempt at the next scheduled
run. The scan's
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

## Statistics

**Maintenance › Statistics** counts the Janitor's runs over the last 7 or 30
days, by kind (packs, dependency and image updates, security fixes) and by
project. Each run is in one column. A finished run counts as Shipped, Nothing
to change, Failed or Closed (skipped, replaced or closed by a person). A run
still open counts as Waiting on a person when Needs you shows its change for
a decision, otherwise as Running. **Why not shipped** groups waiting and
failed runs by the step that stopped them, with the latest reason and links
to examples; a failed run whose change was cleaned up keeps its date without
a link.

**Time to patch** runs from the first AppTown scan that found a vulnerability
to the first usable scan of the running app without it after its latest
sighting, for the same deployment part and scanner. It includes the wait for
a fix to be published, which AppTown does not record, and the time between
scans. Still open counts only vulnerabilities a running-app scan found in the
period.
