# ADR: Interactive vs pipeline — where the workflow seam belongs

Status: **proposed, under review** (2026-09-02). Companion to
design/subsystems/console.md (the chat surfaces), design/subsystems/dev-loop.md (the Change),
and design/subsystems/composition.md (workflow calls). The first audience
is people who are not developers. Prompted by a week of live use: the machinery is good; the *joint*
between chat and workflows keeps failing.

## Context

The forge has two kinds of time, and one mechanism trying to serve both.

**Conversation time**: seconds-scale, iterative, a human present, context
accumulating turn by turn in a session. **Pipeline time**: minutes-to-hours,
unattended, cold-start agents, deterministic stages, evidence and gates.
Workflows are pipeline-time machinery, and they are *good at it* — landing is
a pipeline-time event and landing works.

The failure pattern is everything interactive being forced to round-trip
through pipeline time. The project chat's single mutation is `forge invoke`:
to get anything done, the helper must serialize a warm conversation into
`--title/--body/--intent`, cold-start a workflow agent from that summary, and
then narrate a machine it cannot touch. Four independent failures in one week
were all this seam:

- **Run 184** (recipe-database): chat invoked `implement` without an accepted
  spec-final; the run died at dispatch and sat "running" while the chat told
  the operator "Started! Go watch the project page." Fixed with an
  invoke-time preflight — a bridge repair.
- The chat went **silent after handoff**; fixed by steering the helper to
  follow runs with `forge wait` — a bridge repair.
- Runs wedged on failed gates **presented as healthy** ("running"); fixed
  with the derived blocked state — a bridge repair.
- Handoffs are **lossy**: what the operator actually wants lives in the
  conversation; the build agent re-derives it from a three-line body, and
  misalignment surfaces a whole pipeline later, at review.

When patches keep accumulating on one joint, the joint is load-bearing in
the wrong place. Meanwhile the parts that feel *right* — steering, landing
checks, the Change, landing itself — are all **boundary** machinery. That is
the forge's positioning: evidence and governance at the boundary, not
orchestration in the middle. Interactive workflows are orchestration-in-the-
middle sneaking back in.

There is also an existence proof. The change console already does warm
interactive work: writable worktree, recorded transcript, session resume,
`forge check`, `forge dev`, needs provided. None of the seam failures occur
there, because there is no seam.

## Decision (proposed)

**Draw the seam at the Change. Inside a Change is conversation time; crossing
the landing is pipeline time.**

1. **The change chat becomes the primary interactive surface.** The project
   chat's job shrinks to routing: help the operator decide, open a Change,
   and drop them into its chat. The *same session* that understood what the
   person wants then does the work in the worktree — spec conversationally,
   implement, preview, iterate — warm context the whole way. Heavy lifting
   delegates to subagents (pi-subagents, already baked) under the session's
   context umbrella.

2. **Landing stays exactly as it is** — batch, cold, gated: boot rehearsal,
   tests, scans, guidance conformance, review, human approval. The
   interactive session cannot merge; it can only *submit to landing*. Every
   governance property survives, concentrated at the one boundary where it
   pays.

3. **Workflows remain first-class for unattended work**: landing itself,
   dependency uplift, scheduled scans, excavate, fix-from-complaint, and
   fire-and-forget mode ("just build X, report when done"). The two modes
   split by one test: *is a human present at initiation?* Present → change
   chat. Absent → invoke, cold, as today.

4. **The escape hatch runs batch → interactive, never the reverse**.
   Pipelines for the bulk, conversation for the residue. A bug-sweeper
   workflow scans the tracker and opens a cold build per issue; a human
   reviews each result — right → land, wrong → reject, **nearly right →
   open that change's chat and finish it warm**. Fleet ops are the same
   shape at scale: a batch workflow across fifty repos, forty-five land
   clean, the five stragglers surface blocked and each repo's owner
   resolves theirs in its change chat. The mechanics already exist: a
   batch-created Change has a branch and worktree like any other, the
   change chat attaches to any change, and `retry -c` proves a batch
   agent's session can resume with memory intact. Note the asymmetry: a
   conversation never hands off to a pipeline mid-flight (that was the
   failing seam); a stuck pipeline hands off to a conversation — which is
   where a stuck thing should go, to a human with context.

5. **Discipline moves from sequencing to evidence.** Today the spec
   discipline is enforced by *invoke-time ordering* (product-spec, accept,
   then implement) — which is exactly what kept jamming the chat. Instead it
   is enforced where everything else already is: **at the gate**. A landing
   (or accept) gate requires a spec artifact for change types that warrant
   one. Same guarantee, correct enforcement point.

## Spec recovery from the session

The underlying intuition: **development is the process of discovering
what you want** — a full understanding rarely exists up front. A spec
demanded before work starts is therefore a fiction ratified early; the real
spec emerges through building and reacting. So the spec does not have to be
authored as a ceremony before work starts; it can be **recovered from the
conversation, semi-automatically**. The conversation IS the requirements
elicitation — what the operator asked for, what got negotiated, what got
explicitly excluded, what got discovered along the way. Today that
intelligence evaporates; the distiller should capture it.

Mechanically this rides an existing pattern: `distillNarrative`
(narrative.go) already runs a fast-slot model over a change's evidence after
console turns and run events to keep the "so far" note fresh. Add a sibling,
**`distillSpec`**:

- Input: the change-chat transcript (+ the change body/intent). Output: a
  structured spec — what to build, decisions made, explicitly out of scope,
  acceptance criteria — written as a `spec` artifact on the Change and kept
  fresh as the conversation evolves (re-distilled on meaningful turns, like
  the narrative).
- **Semi-automatic, not automatic**: the operator (or approver) confirms it.
  The natural moment is the accept gate — the approver sees "what we agreed"
  next to "what got built" and blesses both. Confirmation promotes the
  artifact (`spec` → `spec-final`), which is exactly the kind the existing
  `requires:` machinery and gates already understand.
- The batch path is unchanged: product-spec still produces spec-final for
  fire-and-forget builds. Recovered specs and authored specs are the same
  artifact kind arriving by different roads.

This turns the spec from a toll booth in front of the work into a receipt
generated by the work — without giving up the gate that demands it.

### Lenses: what the distiller captures — and at what altitude

The canonical example: the operator says "make the font size 18." The
spec-worthy content is not the px value — it is the requirement the words
serve: "the price must be prominent and easy to find." The distiller's job
is not filtering but **lifting**: the user speaks at implementation
altitude; the spec records what would have to stay true for a future
version to still be right. The decisive check is the **rebuild test**: if
we threw the code away and rebuilt from the spec alone, would omitting this
line make the rebuild *wrong* — or merely *different*? Wrong → spec.
Different → not spec (the px value already lives in the code and the diff;
nothing is lost, it just has a different shelf).

Spec recovery is therefore a **routing problem** across shelves the forge
already owns. Each lens looks for one kind of durable material:

1. **Purpose** — the job this does for a person → spec.
2. **Behavior** — what it observably does; acceptance criteria → spec.
3. **Constraints** — what must STAY true (the betrayal test: would
   violating this later feel like breaking a promise?) → spec.
4. **Exclusions** — what we said no to, with the why → spec. Conversations
   are full of these and they evaporate first.
5. **Decisions** — choices among real alternatives, with reasons → **ADRs**
   (the shelf exists; do not bloat the spec with tradeoffs).
6. **Taste** — recurring preferences not about this feature → project
   **guidance** (taste made durable is what guidance is).
7. **Ephemera** — px values, retries, the bug being swatted → nowhere new;
   the change record already holds it.

**The null verdict is the majority case and must be first-class.** Most
changes do not influence the spec at all. An LLM biased toward
producing output will restate ephemera as fake requirements and rot the
spec into a change-log — the same failure mode the guidance-checks
calibration posture guards against. Countermeasures: the rubric demands the
rebuild test pass AND a cited quote from the conversation as evidence; spec
updates are proposed as **minimal diffs against the existing spec** and
shown as diffs at the accept gate; "no spec change" is the cheap, silent
default.

Structural consequence: the spec is **project-level and living**; each
change contributes a confirmed *delta* ("prominent price" belongs to the
product, not to change #152). Symmetry worth building on: **excavate**
recovers requirements from code; **distillSpec** recovers them from
conversation — two recovery roads into one living document, which is the
"development is discovering what you want" intuition operationalized.

### Lenses are configurable at the instance level — and recovery is 1 → N

Industries and orgs have spec dialects ("capture specs in ABCD format",
EARS, user stories, regulated formats), and different KINDS of durable
material deserve different extractors. Spec recovery is therefore a
**fan-out**: the instance has an *enabled set* of lenses; each enabled lens
runs as its **own invocation** over the same transcript; outputs land on
shelves by lens *type*. Nothing new needs inventing — the pieces ride
existing patterns:

- **A lens is a typed, versioned document — its own entity, NOT a guidance
  block**. It rides the stock/custom origin *pattern* (repo-synced
  stock, forkable custom, versioning, provenance chips — the same pattern
  dependency kinds and workflows use), but it lives outside the guidance
  namespace, is not importable by projects, and is **never injected into
  any agent's steering**. The distiller is its only consumer. This is
  **observer independence**: guidance steers generation; a lens reads what
  naturally happened. If the lens rubric reached the working agent, the
  conversation would start performing for the extractor — teaching to the
  test — and the transcript would stop being honest evidence. Its body is
  a rubric + output format; its **type** declares what it produces and
  where the output routes and gets confirmed: `spec` → spec deltas at the
  accept gate; `adr` → decision records; `guidance` → proposed taste
  blocks; further types (security, compliance) as they earn their keep.
  Stock ships the defaults: a `spec` lens carrying the
  purpose/behavior/constraints/exclusions taxonomy above, the rebuild test,
  the null-verdict posture, and the citation rule.
- **Gaps surface; they are not backfilled.** With full separation, a
  conversation may lack what a lens looks for (a compliance lens wants
  data-retention discussed; nobody discussed it). The lens reports the gap
  ("no evidence on X") and the gate shows it — the fix is never to leak
  the lens into steering. An org that wants conversations to *elicit*
  certain topics writes that as deliberate, separate guidance: explicit
  policy, not an accident of shared storage.
- **The instance-level setting is a SET, not a slot.** Stock lenses ship
  enabled; custom lenses are added alongside. **Additive, not
  replacing**: for load-bearing types (security), the stock lens and the
  org's custom lens run side by side in separate invocations — an org's
  dialect can never silently switch off a platform guarantee. Forking
  stock to make a dialect is still replace-not-patch *per document*; the
  set is where addition happens.
- **Separate invocations, deliberately** — not one merged mega-prompt.
  Independent judgments (each lens blind to the others — the multi-modal
  sweep lesson), independent receipts, parallelizable, and one lens
  erroring doesn't take down the rest. Most lenses return "no delta" on
  most changes; that null verdict is now enforced per-lens, cheap on the
  fast slot.
- **Same-type coexistence is fine**: two `spec` lenses both propose deltas;
  both arrive at the accept gate labeled by their lens, and the approver
  confirms each. Ephemera is what no lens claims.
- **Receipts**: every delta records the lens that produced it —
  name@version + body hash, same fingerprint discipline as conformance
  findings — so "under which rules was this recovered?" has an exact,
  versioned answer per lens. The requirements *process* is evidenced, not
  just the requirements.

On a small instance with first-time users none of this surfaces: the stock set, no additions,
no knobs.

### The general concept: change review

A lens invocation, abstractly, is a **typed, versioned reader over a
change's material, producing typed output for a confirmation path**. The
forge already has several of those wearing different uniforms:

| Reader | Material | Output | Posture |
|---|---|---|---|
| Guidance conformance checks | diff | findings | sh gates, llm advisory |
| Review agent (agent/review) | report + diff | verdict | gates |
| Scanners (osv, gitleaks, sbom) | tree | typed reports | gate by policy |
| Narrative distiller | evidence | "so far" note | automatic |
| **Spec recovery** | **conversation** | **spec deltas** | **confirm at accept** |
| (future: security, compliance…) | any of the above | typed deltas | by type |

**Change review** is the unifying concept: every change is read by a
configurable panel of reviewers — machine and human — each defined by a
versioned lens document, each leaving name@version receipts, each with a
posture (gates / advisory / confirm-at-gate / automatic) set by its *type*.
Spec recovery is the instance whose material is the conversation. The
instance-level lens set IS the review panel.

Posture for getting there: **converge, don't rewrite.** The existing
mechanisms work and carry receipts already; new readers (spec recovery
first) are built on the general shape, and the old ones migrate onto it
only if and when that pays. The concept earns its keep as the mental model
and the config surface, not as a big-bang refactor.

## What changes, concretely

- **Project chat steering**: from "propose an invocation and wait for go" to
  "open a Change and continue there" for buildable asks. `forge invoke`
  stays for fire-and-forget and non-interactive workflows.
- **Change chat**: already writable; gains the working-agent steering
  (implement per skill/implementation, stick the landing: formatter, tests,
  `forge check`, `forge dev` rehearsal) and a **submit-to-landing** verb.
  It cannot approve, merge, or deploy.
- **`distillSpec`** on the change console loop (sibling of
  `distillNarrative`), artifact kind `spec`, promoted to `spec-final` on
  confirmation at the accept/landing gate.
- **Transitional, keep regardless**: `invoke --from-session` — a warm
  handoff where the dispatched build agent resumes (or receives) the chat's
  pi session instead of cold-starting from `--body`. Fixes context loss for
  the fire-and-forget path; pi session resume already exists (`retry -c`,
  console reopen).
- **Landing gates**: optionally require a `spec-final` artifact by change
  type / pack policy (the existing artifact-require step; no new machinery).

## What deliberately does not change

- Landing: batch, cold, gated, human-approved. Untouched.
- Steering, guidance blocks, conformance checks, `forge check`.
- The Change as the unit of work and of evidence.
- Workflows for unattended/scheduled/batch work, and workflow composition.
- The one-mutation posture of the *project* chat (it routes; it still cannot
  edit code).

## Costs, honestly

- **Long-lived working sessions on shared hardware.** Governed by the
  existing console lifecycle/reaper (2h idle TTL, resume on reopen); the
  working set is the same containers the batch path would have used.
- **Mid-loop evidence becomes transcript-shaped.** Run receipts remain for
  everything that gates. The position this document takes: evidence must
  prove what *landed*, not how the sausage was made mid-loop — which is the
  forge's stated posture everywhere else.
- **The change chat has to be good.** The interactive agent must actually
  stick landings (the guidance now says so) or blocked landings shift from
  "dispatch failures" to "quality failures". Mitigation: `forge check` +
  `forge dev` in-loop, and the landing gate as the backstop it already is.
- **Two working modes to explain** (chat-first vs fire-and-forget). For
  first-time users the chat-first story is strictly simpler: "we build it
  together here; when it's ready the forge checks everything and asks the
  approver."
  Fire-and-forget is the advanced mode.

## Migration path

1. **Phase 1 — no code**: re-steer the project chat to route buildable asks
   into a change chat instead of invoking implement. Watch a real build
   happen chat-first end to end. (The steering + console machinery for this
   exists today.)
2. **Phase 2 — spec recovery**: `distillSpec` + `spec` artifact + promotion
   on accept. Turn on the spec-required gate for `feature` changes once the
   distiller proves out.
3. **Phase 3 — warm handoff**: `invoke --from-session` for the batch path.
4. **Phase 4 — polish the primary surface**: submit-to-landing verb in the
   change chat; change-chat capabilities parity audit (needs, preview,
   check) so the interactive loop never has a reason to leave.

## Resolved in review (2026-09-02)

- **Spec confirmation lives at the accept gate.** One blessing covers spec
  and work: the approver sees "what we agreed" beside "what got built" and
  approving promotes the recovered spec to `spec-final`. The distilled spec
  stays visible in the chat as it evolves, so drift is noticed casually
  before the gate ever sees it.
- **Commit as you go, structured.** The interactive agent uses real git
  discipline (small commits, provenance trailers), and the branch is swept
  at landing as today; whether branches get squashed at land is a knob, not
  a decision this document needs.
- **Which change types require a confirmed spec at the gate**: `feature`
  yes — the conversation contains genuine decisions (scope, exclusions)
  that must not evaporate; `fix`/`chore` no — the change is its own
  description, a spec would be ceremony. Set as **pack policy**, not
  hardcoded: an enterprise pack can require specs on everything; a pack for
  first-time users stays light.
- **Fire-and-forget survives — as the batch entry point, not a degraded
  interactive mode.** Nobody chats with a fleet migration. The bug-sweeper
  and fleet examples (Decision §4) are fire-and-forget by nature, with the
  batch→interactive escalation waiting for the nearly-right residue.
  `--from-session` upgrades the case where a batch build is launched from a
  live conversation.
