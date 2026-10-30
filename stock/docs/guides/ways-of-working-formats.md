# Ways-of-working formats

A way of working is data: workflows, packs, policies and guidance blocks with
their checks. This page says which shape each takes when you write one, how AppTown reads it, and what it promises to keep reading.

## What you write, and where

| Content | You write it as | Through |
| --- | --- | --- |
| Workflow | YAML, `apiVersion: apptown.dev/v1` | the Workflows page, or `POST /api/v1/workflows/library` |
| Pack | the Packs page's form (JSON underneath) | `POST /api/v1/packs` |
| Policy | a library entry: a rule and its options | the Policies page |
| Guidance block | markdown, with tags and checks | the Guidance page, or the block API |

Everything is checked when you save it. A key the format does not have is an
error that names it, never a field silently ignored: `severty` for `severity`
in a check, `gates` for `gate` in a stage, `autoAdopt` on a pack.

## Workflows

The schema is served at `GET /api/v1/workflows/schema` for editors and agents;
`POST /api/v1/workflows/validate` checks a draft without saving it and says
what is wrong, including the rules a schema cannot express (unique stage and
step names, gates naming steps that exist).

- `apiVersion: apptown.dev/v1` is the format; leaving it out means the
  same. Any other value is refused by name.
- Top-level keys are `apiVersion`, `kind`, `metadata` and `spec`. A top-level
  key starting `x-` is ignored, so it can hold YAML anchors you reuse:

  ```yaml
  x-review: &review
    prompt: Check the change against its intent
  spec:
    stages:
      - name: review
        steps:
          - name: look
            uses: agent/review
            with: *review
  ```

- Each step kind takes only its own `with` options: an approval takes
  `approvers`, `quorum`, `means`, `instructions` and `inAdvance`, a tests step
  `command`, `require` and `timeoutMinutes`, and so on (the served schema lists
  every kind's). Any other key is refused by name, in any casing; `minApprovals`
  is not `quorum`. Where an option's value is data (a script's `env`, a called
  workflow's `with`) its keys are yours.
- An approval's `inAdvance` is experimental. It lets the person opening an
  adoption change say now that their yes counts if the change turns out to
  need no code. Whatever sentence the author writes, AppTown casts that yes
  only when the change is empty against its base and every step other than
  an approval, and every check, passed outright (no override, skip, error,
  excused finding or check it could not run). Such a yes is the person's one
  vote; it never stands in for another approval the workflow requires.
- `failOn`, on a `scan` or `guidance/conformance` step, is `low`, `medium`,
  `high` or `critical`; anything else is refused by name. Left out, a scan
  fails on every finding it may gate on (whatever its scanner looks at; the
  stock image scans write `failOn: high`), and a conformance check fails at
  `high`.
- Steps read the change as `${{change.title}}`, `${{change.body}}`,
  `${{change.type}}` and `${{change.id}}`. `${{workItem.*}}` is refused in a
  save.

## Packs

A pack names guidance blocks, workflows and policies from the library. A
project takes one or more packs and gets all of them: every pack's guidance,
workflows, policies, tools, services and extensions. Where two packs disagree
on something a project has one of (a tool's version line, the toolchain, a
service of the same name, a policy of the same name, or starter files), they
cannot be taken together, and AppTown names both. No pack outranks another,
and a pack does not include another: put what several packs share in a pack
of its own, as `server-base` is. A pack may state
`apiVersion: apptown.dev/v1`; leaving it out means the same, and any other value
is refused by name. Stating it does not make a new version. A pack names policies; it
does not define them: add a policy to the library first, then pick it. Every
block, workflow and policy a pack names must be in the library when you save
it, so its version can pin it; one that is not is refused by name. (A
language pack lists system workflows such as `land` without pinning them:
the platform pack pins those.)

Each saved version records exactly what it contains: each block and workflow at
a version, and each policy's definition as it was when the version was
published. Changing a library policy later does not change an existing pack
version; AppTown publishes a new version of each pack that names it, which
you recommend when you are ready.

A pack's toolchain is a starter `mise.toml` copied into a new project when
the pack is applied to it; a project that already has commits keeps its own.
The pack version does not pin the file: a later release's toolchain of that
name is what a later project gets, and once copied the file is the
project's own.

`requires` names instance capabilities a project on the pack needs (a scanner);
adopting the pack is refused until each is attached.

A pack declares no memory. A part's memory and CPU come from its
deployment's override, else what the app declares for its profile in
`.apptown/app.yaml`, else AppTown's default; a pack's guidance can say what
to declare. A dependency catalogue kind may declare
`resources: {memory: 256M}` (`M` and `G` are read as MiB and GiB there) as
the memory its local service gets when the app declares none, separate from
need parameters. The declaration schema is
`stock/docs/api/memory-default.schema.json`; the owner additionally validates the
minimum (32 MiB) and numeric range.

## Policies

| Rule | Options |
| --- | --- |
| `requireStepBefore` | `uses`, `requires`: step kinds; a `uses` step needs a `requires` step in an earlier stage |
| `requireCapabilityBound` | `capability`: the instance must have a binding for it |
| `scanFailOnCeiling` | `max`: `low`, `medium`, `high` or `critical`; no scan step may be looser |

An option the rule does not take is refused when you save the policy.

## Guidance blocks and checks

A block is markdown. It may state `apiVersion: apptown.dev/v1` (in its front
matter, or beside `name` when created or edited through the API), which covers
the block and its checks; leaving it out means the same. Its checks are a JSON array; each check has a `name`, an
`expects` sentence, a `severity` (`note`, `low`, `medium`, `high`) and one
evaluator: `sh` (a script), `llm` (a rubric), `runtime` (a probe of the running
app) or `tool` (a scanner binding). The [conformance guide](conformance.md) says
what each evaluator proves.

A block may carry a **skill**: a how-to an agent loads only when it does
that one thing. One line in the body splits the two parts:

```markdown
The rules every agent reads.

<!-- skill: How to write the Dockerfile. Load it before editing a Dockerfile. -->

The how-to.
```

Above the line is what every agent reads up front, followed by a line
naming the skill. Below it is the skill. AppTown hands the skill to its
agents as an [Agent Skills](https://agentskills.io/specification) `SKILL.md`
in the agent's home, at `~/.agents/skills/` and `~/.claude/skills/`, named
after the block (`build/container-image` gives `build-container-image`). The
description is what the agent decides by, so it says what the how-to covers
and when to load it. A block has at most one such line; its name must make
a skill name (lowercase letters, digits, single hyphens, at most 64
characters), and two adopted blocks cannot give the same skill name. The
block's checks run the same whether or not an agent loaded its skill.

An `sh` check is a script run with `sh -c` in a container, with the
project's files read-only, no network, and at most 60 seconds. Exit 0 means
the project conforms; exit 1 is a finding, and what the script printed is the
finding's text. Any other exit, a timeout, or no answer means the check
could not assess the project: an error, shown as such and never a pass. The
container image (today `alpine/git` with a shell and Git) is not promised:
use only POSIX shell and Git.

Every check states **`appliesTo`**, a list of words; it is required, and a
check without it is refused when it is saved:

| Word | The check applies to | It runs |
| --- | --- | --- |
| `repository` | the repository, when it holds any file (stands alone) | once, at the root |
| `part` | every part | in each part's folder |
| `build` | parts built from a folder's Dockerfile (`build:`, `static: {build:}`) | in that folder |
| `static` | parts served as files (`static:`) | in the static folder or its build folder |
| `container` | parts run as containers (`build:`, `image:`) | in the build folder; at the root for an `image:` part |
| `tool:<name>` | parts whose `tools:` in `.apptown/app.yaml` name the tool | in the part's folder |

A part matches when it matches any of the words. `<name>` is a tool the
tool versions list has (go, node, python, java, dotnet, ruby, rust, php,
godot). A check that applies to nothing the app declares is **not
applicable**, with the reason, before anything runs: never a pass, never an
error. An `sh` or `tool` check runs once in each folder of the parts it
matches (parts sharing a folder are judged once, together) and its outcome
is the worst of them; a rubric or a runtime check is judged once when any
part matches. Once a check applies, **finding nothing to judge is a
finding** (exit 1, saying what is missing): the contract says there is
something there.

A script starts in its folder and gets: `APPTOWN_FOLDER` (the folder,
relative to the root; `.` for the root), `APPTOWN_ROOT` (the root, for
files the repository declares there, such as `.apptown/app.yaml` and
`.gitignore`), `APPTOWN_PARTS_HERE` (the parts judged in this folder,
space-separated), `APPTOWN_PARTS` (every part, one line each:
name, tab, folder, tab, its tools comma-separated) and
`APPTOWN_NOT_APP_CODE` (the contract's `notAppCode` folders, one per line).
A tool's inputs are at or below the folder: a Go part at the root may build
its UI from `frontend/`, so a rule about a tool's files looks below the
folder, not only in it.

## What stays readable

AppTown keeps running what it accepted. A workflow version, a pack version
and a block version are publications: they are never rewritten, and the stricter
rules on this page apply to what is saved from now on, not to history.
