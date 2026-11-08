# Features

Setup → Features lists switchable areas in On by default and Beta groups,
with their current choices, what they cover and, behind a ? beside each
switch, what turning it off does. The Features guide lists all areas. An administrator changes a switch for the whole AppTown. Every
change records the administrator and the before and after choices together.

The command line uses the same API:

```
apptown features
apptown features on evals
apptown features off conformance
```

`GET /api/v1/features` lists the areas. `PUT /api/v1/features/{area}` takes
`{"expected": false, "enabled": true}`. A stale expected choice is refused;
reload before saving again. Always-on areas have no off switch.

| Area key | Tier | What it covers |
| --- | --- | --- |
| projects | Always on | Projects and repositories |
| changes | Always on | Changes, runs and adopted workflows |
| landing | Always on | Required workflow checks and acceptance |
| releases | Always on | Releases, promotion and rollback |
| deployments | Always on | Deployments, built-in Docker and Docker over SSH targets |
| people | Always on | People, sign-in, tokens and authority checks |
| record | Always on | History, decisions, operations and evidence |
| setup | Always on | Instance settings, credentials and Features |
| conformance | On by default | Estate reviews, verdicts and fixes; each change's standards check runs either way |
| backups | On by default | Backups and restores of your apps' data |
| apptown-backup | On by default | The daily dump of AppTown's own database |
| ways | Always on | Packs, workflows, guidance and policy pages |
| chats | Always on | Interactive change chats |
| usage | Always on | Usage and disk pages |
| worker | Always on | Worker settings and observation |
| previews | Always on | Change previews |
| models | Always on | Model setup |
| updates | On by default | Checking for new AppTown versions, and the Updates page |
| complaints | Always on | Reports about AppTown |
| evals | Beta, off by default | Evaluations |
| what-ifs | Beta, off by default | Projects behind on a pack, and trying a newer version first |
| restore-rehearsals | Beta, off by default | Rehearsal and restored-copy environments |
| conformance-estate | Beta, off by default | Estate conformance inventory |
| app-platform | Beta, off by default | DigitalOcean App Platform targets |
| portainer | Beta, off by default | Portainer targets |
| bucket-site | Beta, off by default | S3 bucket sites |
| mcp | Beta, off by default | MCP access for outside agents |
| memory | Beta, off by default | Project notes and memory pages |

## What turning each one off does

The ? beside each switch on the Features page says the same.

- **Conformance** (conformance): Hides the Conformance pages and refuses estate reviews, verdict confirmations and fixes from the web, the command line and outside agents. Adoption no longer starts reviews and no new fix items are made. Each change's own standards check still runs whenever its adopted workflow requires it. Past reviews and verdicts are kept and come back when it is turned on again; Pack upgrades and the estate view need it on too.
- **Deployment backups** (backups): Pauses backups of your apps' data: no scheduled or after-release backups and no scheduled cloud copies. The Backups pages, Back up now and restores are refused, and restore rehearsals with them. Use it when your apps' data is backed up another way, such as filesystem snapshots. Existing backups are kept; turning it on again lets the next scheduled pass back up again. Work already started finishes. AppTown's own backups have their own switch.
- **AppTown's own backups** (apptown-backup): Stops the daily dump of AppTown's own built-in database, kept in its data folder. Use it when AppTown itself is backed up another way, such as filesystem snapshots of its host. Dumps already taken are kept, and a whole-AppTown backup by hand (apptownd backup) still works. Turning it on again resumes the daily dump. Your apps' backups have their own switch.
- **Update checks** (updates): AppTown stops checking for new versions and the Updates page is hidden. Use it for an install upgraded through its own tooling or with no internet access. Nothing installed changes; turning it on again resumes the checks.
- **Evals** (evals): Hides evals and starts no new eval runs. Runs already started finish and their results are kept.
- **Pack upgrades** (what-ifs): Hides Pack upgrades: the projects behind on a pack, and what-ifs that try a newer pack version on them before anyone adopts it. Adoption changes already opened from a what-if are ordinary changes and carry on. Needs Conformance on.
- **Restore rehearsals** (restore-rehearsals): Starts no new restore rehearsals or restored copies. Rehearsal places already made are still cleaned up, and past results are kept.
- **Conformance estate view** (conformance-estate): Hides the estate-wide conformance inventory. Each project's own conformance pages are unaffected. Needs Conformance on.
- **App Platform targets** (app-platform): DigitalOcean App Platform can no longer be chosen or set up as a target. Deployments already on it keep running, and their history and start, stop and restart stay available.
- **Portainer targets** (portainer): Portainer can no longer be chosen or set up as a target. Deployments already on it keep running, and their history and start, stop and restart stay available.
- **Bucket-site targets** (bucket-site): S3 bucket sites can no longer be chosen or set up as targets. Sites already published keep serving, and their history stays available.
- **MCP for outside agents** (mcp): Outside coding agents can no longer connect over MCP; their requests are refused. AppTown's own agents and chats are unaffected.
- **Project memory** (memory): Hides project notes and memory pages. Decisions stay part of the record, and notes already in a repository stay there.

Turning an area off removes its navigation and controls and refuses requests
through the API, CLI and MCP. The refusal names the area and says an administrator
can turn it on in Features. Records are retained. Decisions remain part of the
record even when project notes are off.

A switch never removes a step from an adopted workflow. Turning conformance off
hides estate views, reviews and remediation. A workflow's landing guidance check
still runs when that workflow requires it. Choose a workflow without that step
to change that requirement.

Target switches control target setup and selection. Core deployment history and
runtime controls remain available.

Chats, models and the worker are always on: they are how changes are made, and
hiding their setup would only hide what keeps running.

Beta pages show a small beta label. Some areas work within another and are off
while it is off: Pack upgrades and the conformance estate view need conformance, and
restore rehearsals need backups. Features shows such a switch greyed with what
it needs; its own choice is kept for when that area is on again. On upgrades, a data migration preserves beta areas
with stored use. Existing projects keep memory enabled because imported
repositories can contain notes without a separate usage record. New installations
start with beta areas off.

Off areas start no fresh optional background work. Deployment backups off is for apps backed up another way (for example, filesystem
snapshots): it stops daily backup requests, scheduled cloud copies and
after-release backups. AppTown's own backups off stops the daily built-in
database dump. Conformance off stops adoption-triggered reviews and
new remediation items. Evals off stops new arms. Deployment backups off stops its optional background observations.
Preview and disk observations always run. Updates off stops
automatic release discovery. Connection checks for scanners and cloud accounts
follow their setup area's switch.

Work already started can finish. Required workflow effects, deployment runtime
observation, usage accounting, and cleanup continue, including moving existing
backup sets to their store, removing rehearsal environments and retiring eval
copies' caches. Turning an area off does not cancel queued requests or remove
their records. Turning it on lets its next scheduled pass start work again.
