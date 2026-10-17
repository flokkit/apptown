# Ways-of-working formats

A way of working is data: workflows, packs, policies and guidance blocks with
their checks. This page says which shape each takes when you write one, how the
forge reads it, and what it promises to keep reading.

## What you write, and where

| Content | You write it as | Through |
| --- | --- | --- |
| Workflow | YAML, `apiVersion: forge.flokkit.io/v1` | the Workflows page, or `POST /api/v1/workflows/library` |
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

- `apiVersion: forge.flokkit.io/v1` is the format; leaving it out means the
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

- Steps read the change as `${{change.title}}`, `${{change.body}}`,
  `${{change.type}}` and `${{change.id}}`. The older name `${{workItem.*}}` is
  refused in a save. Workflow versions saved before 28 September 2026 that use
  it keep running as they are.

## Packs

A pack names guidance blocks, workflows and policies from the library, and can
include another pack with items replaced or dropped. A pack names policies; it
does not define them: add a policy to the library first, then pick it.

Each saved version records exactly what it contains: each block and workflow at
a version, and each policy's definition as it was when the version was
published. Changing a library policy later does not change an existing pack
version; the forge publishes a new version of each pack that names it, which
you recommend when you are ready. Versions published before October 2026
recorded policy names only, and read the library's current definition when
applied.

`requires` names instance capabilities a project on the pack needs (a scanner);
adopting the pack is refused until each is attached.

A pack may declare `resources: {memory: 160M}` as the starting memory default
for an undeclared app part. Its immutable published version records that default;
a project's primary adopted pack supplies it, never the latest recommendation.
Included packs inherit it, an including declaration replaces it, and
`clears: [resources]` removes the inherited default. A valid older version with no
field supplies no default. CPU, environment and unlimited-memory choices are not
pack resource defaults. The reusable declaration schema is
`stock/docs/api/memory-default.schema.json`; the owner additionally validates the
minimum (32 MiB) and numeric range. Catalogue kinds can use the same optional
shape for their local service memory, separate from need parameters.

## Policies

| Rule | Options |
| --- | --- |
| `requireStepBefore` | `uses`, `requires`: step kinds; a `uses` step needs a `requires` step in an earlier stage |
| `requireCapabilityBound` | `capability`: the instance must have a binding for it |
| `scanFailOnCeiling` | `max`: `low`, `medium`, `high` or `critical`; no scan step may be looser |

An option the rule does not take is refused when you save the policy.

## Guidance blocks and checks

A block is markdown. Its checks are a JSON array; each check has a `name`, an
`expects` sentence, a `severity` (`note`, `low`, `medium`, `high`) and one
evaluator: `sh` (a script), `llm` (a rubric), `runtime` (a probe of the running
app) or `tool` (a scanner binding). The [conformance guide](/docs/guides/conformance) says
what each evaluator proves.

## What stays readable

The forge keeps running what it accepted. A workflow version, a pack version
and a block version are publications: they are never rewritten, and the stricter
rules on this page apply to what is saved from now on, not to history.
