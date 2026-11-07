# Features

Setup → Features lists every area, its tier, its current switch, what it covers
and its guide. An administrator changes a switch for the whole AppTown. Every
change records the administrator and the before and after choices together.

The command line uses the same API:

```
apptown features
apptown features on evals
apptown features off chats
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
| conformance | On by default | Reviews, verdicts and remediation |
| backups | On by default | Copies and restoration |
| ways | On by default | Packs, workflows, guidance and policy pages |
| chats | On by default | Interactive change chats |
| usage | On by default | Usage and disk pages |
| worker | On by default | Worker settings |
| previews | On by default | Change previews |
| models | On by default | Model setup pages |
| updates | On by default | Update checks and settings |
| complaints | On by default | Reports about AppTown |
| evals | Beta, off by default | Evaluations |
| what-ifs | Beta, off by default | Proposed guidance comparisons |
| restore-rehearsals | Beta, off by default | Rehearsal and restored-copy environments |
| conformance-estate | Beta, off by default | Estate conformance inventory |
| app-platform | Beta, off by default | DigitalOcean App Platform targets |
| portainer | Beta, off by default | Portainer targets |
| bucket-site | Beta, off by default | S3 bucket sites |
| mcp | Beta, off by default | MCP access for outside agents |
| memory | Beta, off by default | Project notes and memory pages |

Turning an area off removes its navigation and controls and refuses requests
through the API, CLI and MCP. The refusal names the area and says an administrator
can turn it on in Features. Records are retained. Decisions remain part of the
record even when project notes are off.

A switch never removes a step from an adopted workflow. Turning conformance off
hides estate views, reviews and remediation. A workflow's landing guidance check
still runs when that workflow requires it. Choose a workflow without that step
to change that requirement.

Target switches control target setup and selection. Core deployment history and
runtime controls remain available. Existing model and worker bindings still
serve adopted workflows when their setup pages are off.

Beta pages show a small beta label. Conformance estate views and what-ifs also
require conformance to be on. On upgrades, a data migration preserves beta areas
with stored use. Existing projects keep memory enabled because imported
repositories can contain notes without a separate usage record. New installations
start with beta areas off.

Off areas start no fresh optional background work. Backups off stops daily
backup requests, scheduled cloud copies, after-release backups and the daily
built-in database dump. Conformance off stops adoption-triggered reviews and
new remediation items. Evals off stops new arms. Previews, Worker, Usage and
Backups off stop their optional background observations. Updates off stops
automatic release discovery. Connection checks for models, workers, scanners
and cloud accounts follow their setup area's switch.

Work already started can finish. Required workflow effects, deployment runtime
observation, usage accounting, and cleanup continue, including moving existing
backup sets to their store, removing rehearsal environments and retiring eval
copies' caches. Turning an area off does not cancel queued requests or remove
their records. Turning it on lets its next scheduled pass start work again.
