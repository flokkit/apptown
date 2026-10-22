# The change catalog

A workflow cans the process of a change: how it is made and verified. A
**catalog change** cans the change itself: a named definition of a specific
piece of work and how to know it was done right. The release ships them in
`stock/catalog.yaml`, like its workflows, packs and policies; `apptown
catalog` lists them and `apptown apply <catalog-change> -p <project>
[--param k=v]` runs one on a project.

## Terms

- **Workflow**: canned process. ("Lane" is not a separate concept; where the
  distinction matters, say "working workflow".)
- **Catalog change**: a definition in the change catalog. Its `workflow` field
  names the workflow that runs it.
- **Change**: one instance, a catalog change or a free-text task applied to one
  repository, with its ordinary branch, gates, evidence and landing.

The name claims only what is true by construction: written down, shipped with
a release and findable. It does not promise low risk or a verdict; the receipts do that. In
governance terms this is a standard change: pre-authorised, repeatable, with
lighter ceremony per instance.

## What a definition holds

- a name and description (`uplift/go-version`);
- typed parameters, few, that fill the template; there is no free-text body;
- the prompt template the agent receives;
- the workflow that runs it (a definition may require a heavier one);
- the skills the agent gets;
- optionally, paired guidance checks: a catalog change that introduces a
  convention ships the check that enforces it, versioned together.

Where a deterministic engine exists for the transform (OpenRewrite, a codemod,
a language's own fixer), a definition should run it and use the agent to
reconcile, build and report, not to make a mechanical edit by hand.

## Rules

- **The catalog is a file of the release.** An entry is added or changed by
  changing `stock/catalog.yaml`, which is reviewed like any other change to
  AppTown. A run started from an entry records the entry's name and the
  release it came from, which together name the exact definition.
- **The definition's model pin wins over project model slots.**
- **Parameters can tune effort, never verification.** An invocation may set the
  prompt parameters and a short list of workflow knobs (`timeoutMinutes`,
  thinking level). Gates are not on that list.
- **No second execution path.** Applying a catalog change creates an ordinary
  Change and starts its workflow; nothing downstream knows it came from the
  catalog.
- **Ad-hoc work is untouched.** The catalog is for changes worth repeating, not
  a gate on free-text work.

Applying a catalog change across many projects, and a view of where it has
landed, are not built; a fleet apply would be N ordinary invocations.
