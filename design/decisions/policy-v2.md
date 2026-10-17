# Policy v2: rules anyone can follow

Designed 2026-08-30. Status: legibility slice BUILT; the
other axes are staged.

## The bar

Every policy must be explainable to a first-time user who is not a
developer. Not dumbed down — clear. Two questions must always have visible answers:
"what are our rules?" and "why can't I release my software?" A policy
that cannot say why it blocked someone, in one plain sentence with a
what-to-do, is not finished.

Three layers per policy, same layering principle as everywhere:

1. **The sentence** — the author-written description. "Nothing goes
   out without a security scan first."
2. **The precise reading** — rendered by the SERVER from rule+params
   (one source of truth, no UI drift): "any workflow containing a
   `deploy` step must have a `scan` step in a strictly earlier stage;
   checked when workflows are saved."
3. **Standing** — who complies right now: every adopting project,
   in line or not, each violation naming the workflow, the reason, and
   THE FIX ("add a scan step to a stage before the deploy step").

## What stays from v1 (deliberately)

Named rule types in core with parameters — never a predicate language.
The founding comment survives: "a single operator needs perhaps six
rule forms." Rule vocabulary is shared reality (design/subsystems/plugins.md):
growing it is a core change through the core's gates. Sentence-first
descriptions; pack-carried adoption with origins; block-at-save,
warn-per-run.

## The staged axes

- **Vocabulary** (as needed, each a core change): requireStepAfter,
  requireWorkflowExists, requireGuidanceImported (the policy↔guidance
  bridge), requireApprovalBy, allowedModelProviders, releaseRequires
  (promotion preflight), timeoutCeiling.
- **Enforcement points and modes**: each rule type declares where it
  bites (save / run preflight / release preflight / config change);
  each adoption picks warn or enforce → the warn→observe→enforce
  rollout. Instance-wide adoption beside per-project.
- **Waivers**: justified, attributed, time-boxed exceptions reusing the
  suppression machinery. Amber in standing with reason and expiry.
  Governance, not handcuffs.

## Taxonomy (do not merge)

Policies watch the RAILS — the system of work: workflow shapes,
bindings, promotions. Guidance watches the CARGO — the code produced.
Gates are per-run enforcement instances of both. Cross-linked by
requireGuidanceImported; never merged.
