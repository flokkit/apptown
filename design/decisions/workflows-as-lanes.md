# Workflows live in the library; projects select them

Two kinds of workflow share one word:

- **System workflows** are how the forge does its own jobs: landing (with
  shipping as its stage), wiring an integration in or out, the scheduled
  dependency patch. They are marked `metadata.system: true`. The forge invokes
  them by name; a person never picks one, and they are never offered in a "run
  a workflow…" list. Ways of working lists them under a System heading with one
  line on when the forge runs each.
- **Lanes** are ways a person chooses to work on a change: a quick change, an
  implementation, a bug fix, plan-then-execute, a spec, research, an excavation.

## The decision

1. **Workflows live at the instance; projects select, never copy.** The library
   is the only source. A project's way of working is a selection of lanes
   (`project_lanes`, defaulted by its pack) plus the policies it adopts. A run
   resolves the version the project's adoption revision pins and records its
   origin (`stock/quick-change@10`, `lib/our-land@1`); that receipt replaces any
   per-project copy.
2. **Customising is cloning at the instance.** A team clones a workflow into
   its library and selects the clone. An administrator customises a system job
   for the whole instance by binding the stock system name to a custom library
   workflow that is itself marked system (`system_workflows`, audited).
3. **Policies apply where they are adopted**: when a library workflow is saved,
   when a project's selection changes, and when a run resolves its workflow.
4. **System workflows are parametrised by attachments, never edited per
   project.** `land` declares scan points, not scanners: `uses: scan` with
   `on: source` runs every attached scanner that reads source, and `on: image`
   every attached scanner whose target is an image, each as its own step with
   its own receipt. Attaching a scanner puts it into every landing it applies
   to, with no edit to `land`.

The rule this gives the design: **if you need to edit a system workflow, the
forge is missing an attachment point.** Add the point; do not fork the workflow.

## Why

Per-project copies created stale copies on every project, a second editing
surface beside the library's, and policy checks hung on a save event that
nobody needed. Per-project divergence is still possible, through
clone-and-select, and every run says exactly what ran.
