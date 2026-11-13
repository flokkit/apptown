# Janitor

The Janitor page shows what the janitor may change, what it does
when something goes wrong, which projects need a person, and the Changes it
has landed. Nothing runs until a person grants permission.

## What it may change

**Dependency updates** cover weekly patch and minor updates, or nightly updates
including major versions. Security fixes run on their own daily schedule within
the same version limits. Neither level changes language versions or base images.
**Keep packs current** moves projects to newly published pack versions and makes
any application changes those updates need.

Each approval shows its limits, projects, automatic release destinations, who
granted it and when. An administrator uses **Preview** to read the exact meaning
and affected deployments, then **Enable** to grant it. **Revoke** stops new work
under that approval. Work that has already claimed permission may finish.
The Packs page links here for pack update approvals.

A project's own Janitor section controls its update level. Turning it off stops
both dependency and pack updates there. The choice records who changed it, when
and why; it appears below the approval as well as beside the project.

## When something goes wrong

The page reads repair limits from each project's adopted workflows. A failed
check starts the declared repair sessions. Work that still fails waits for a
person; a missing or unknown check never counts as success.

Security fixes, dependency updates and pack updates have separate failure counts.
Each queue shows its workflow version, repair limit and stop rule once. Projects
are listed where they use different adopted versions. Current stopped queues
appear with the project's state. Stopping weekly updates leaves security
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
visible; neither means the project is up to date. Findings confined to running
images have their own count: the janitor does not update base images yet.

**Last 7 days** lists landed Changes, package moves or pack versions and the
releases that contain them. Each Change links to its checks and approval.
**Older history** pages through earlier landings. The janitor's attribution names
the person who granted the original approval and its revision, on the Janitor page and on the Change itself. Work without a
covering approval names its schedule instead. History does not replace the
current deployment or scan evidence.
