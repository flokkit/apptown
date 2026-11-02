# Workflows live in the library; projects choose packs

Two kinds of workflow share one word:

- **System workflows** are how AppTown does its own jobs: landing (with
  shipping as its stage) and wiring an integration in or out. They are marked `metadata.system: true`. AppTown invokes
  them by name; a person never picks one, and they are never offered in a "run
  a workflow…" list. Ways of working lists them under a System heading with one
  line on when the apptown runs each.
- **Lanes** are ways a person chooses to work on a change: a quick change, an
  implementation, a bug fix, plan-then-execute, a spec, research, an excavation.

## The decision

1. **Every project can run every core and extra library workflow**, plus
   workflows its adopted packs bring. Packs are the project's way-of-working
   choice. Workflows have no per-project selection or copies. Each run records
   the exact version and origin it ran.
2. **Customising is cloning at the instance.** A team clones a workflow into
   its library and runs the clone. An administrator customises a system job
   for the instance by binding its name to a custom system workflow
   (`system_workflows`, audited).
3. **Policies apply where they are adopted** and when a run resolves its
   workflow. Authority checks apply to every invocation.
4. **System workflows are parametrised by attachments, never edited per
   project.** `land` declares scan points, not scanners: `uses: scan` with
   `on: source` runs every attached scanner that reads source, and `on: image`
   every attached scanner whose target is an image, each as its own step with
   its own receipt. Attaching a scanner puts it into every landing it applies
   to, with no edit to `land`.

The rule this gives the design: **if you need to edit a system workflow, the
AppTown is missing an attachment point.** Add the point; do not fork the workflow.

## Why

Per-project copies created stale copies on every project, a second editing
surface beside the library's, and policy checks hung on a save event that
nobody needed. Per-project divergence is still possible, through
library clones, and every run says exactly what ran.
