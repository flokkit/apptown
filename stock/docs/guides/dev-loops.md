# Dev loops

How should day-to-day development actually work on this platform? Packs, guidance,
and integrations can steer *how* you work, but they don't answer the core question:
what is the loop a developer (or an agent, or both) goes around all day? This
document maps the options and states the position Forge takes.

## The real difference: when is intent captured?

Two ways of working that look superficially similar are separated by one thing —
whether the statement of intent exists before or after the change.

**Plan-first** captures intent up front: write a spec, then execute it, then verify
the result against it. The paper trail is naturally strong because the intent
artifact predates the code. This suits well-understood work, and it suits agents,
which execute specs better than they explore.

**Iterate-first** (prototyping, exploratory work) captures intent afterwards: you
poke at the problem, converge on something you like, and only then write the MR
description explaining what the change turned out to be. The narrative is
retrospective, because for this kind of work you genuinely don't know the spec
until you've built the thing.

These aren't two maturity levels of the same process. They are different orderings,
and both are legitimate. A platform that only supports plan-first quietly pushes
exploratory work off the platform — into untracked side channels.

## The option space

Four loop shapes, roughly in order of how much structure they impose:

### 1. Plan-first (spec → implement → verify → ship)

What Forge workflows do today. An intent artifact (spec, change request, prompt)
starts the run; agents execute; check/review/ship stages verify and land it.
Strong receipts, poor fit for problems you can't spec in advance.

### 2. Converge, then formalize

Iterate freely in the console. When you're happy with the result, invoke a closing
sequence whose job is *landing*, not authoring: it distills a change narrative from
what actually happened (the diff plus the session trajectory, not your memory of
it), runs the gates, and ships. The MR description is generated from evidence and
approved by you, rather than written from recollection. This is the honest
formalization of how most real development already works.

### 3. Continuous checkpointing

Every micro-iteration passes through a lightweight gate — the trunk-based / TCR
instinct, where no step is ever unverified so there is no big formalization moment
at the end. Structurally appealing, but it fights the grain of exploration, which
involves deliberately broken intermediate states. Not a fit as the primary loop;
useful as an opt-in discipline within one.

### 4. The Change

Today a run equals one workflow execution. The generalization: the unit of work
is a **Change** — born when work starts (`start`: branch cut, workspace
provisioned, evidence trail begins), worked for as long as it takes — console
sessions, agent steps, ad-hoc checks, dead ends included — and finished through
a landing sequence (`land`: narrative, gates, approval, ship) or closed without
landing (`abandon`). Plan-first becomes the special case where the intent
artifact came first and an agent did the work. Iterate-first becomes the case
where a human worked the Change and the narrative was distilled at landing.
The word follows Gerrit's usage: a long-lived unit that accumulates iterations
and ends by merging or abandonment. Every change to the product is a Change,
with receipts.

## Forge's position

Don't pick one inner loop. Fix the boundary instead.

**Freedom in the inner loop, a contract at the landing.** What a landing must
include is non-negotiable: a narrative that matches the diff, green gates, receipts
covering provenance (who or what authored the change, under what steering, with
what tools), and any required approvals. How the work got done inside the frame —
spec-first agent execution, human console iteration, or turns of each — is up to
the pack and the person.

Two consequences worth stating plainly:

- **Provenance is recorded, not laundered.** A change authored interactively in the
  console carries `seeded/console` provenance, not a pretend agent-authored trail.
  The distinction is part of the record.
- **The retrospective ordering improves spec quality.** The closing sequence can
  distill a crystallized intent document from the trajectory — what this change
  turned out to be for — reviewed for accuracy at the moment you actually know the
  answer. Plan-first promises that artifact up front; iterate-first delivers it at
  close.

## The lanes

The loop above is packaged as a small catalog of lanes — workflows named by
intent, not mechanism. Every lane works on the Change's branch and ends at a
human gate; only the landing merges.

- **Fix a bug** — the agent must reproduce first (a failing test, committed)
  and a judge confirms the reproduction is genuine before any fix is
  attempted. "Cannot reproduce" stops the lane honestly. The receipt reads:
  failing repro → fix → same repro passing.
- **Quick change** — a small described task with one approval.
- **Plan & execute** — for broader scope: the agent drafts a plan with
  acceptance criteria, you approve *the plan*, execution is then judged
  against those criteria item by item.
- **Implement** (`forge build`) — an application or a substantial new
  capability from a description or an accepted spec; the project's
  attached integrations are wired in from steering.
- **Dependency uplift** — patch/minor updates verified by build+tests, majors
  surfaced with changelogs, the result proven clean by a dependency scan.
- **Adopt / retire an integration** — when the organisation attaches or
  retires a cross-cutting service, wire it in (or prove it fully unwired).
- **Adopt a branch** (`forge import <branch>`, or Settings › General on the
  project page) — adopt work done outside the loop as a Change. Its history
  is graded honestly (reconstructed, not witnessed) and it lands through the
  same contract as everything else.

## The loop from a terminal

The CLI stays with the work — one command from intent to shipped:

    forge fix "the /pulse endpoint reports the wrong service name"
    # streams the reproduction, shows REPRO.md and FIX.md, asks you to
    # approve, offers the landing, ends at:
    #   ⚑ change #57 shipped · release #8 · the main URL

    forge status          # what's waiting on you, what's in flight
    forge changes         # what's in flight and what each waits on (--all: shipped too)
    forge show 57         # a change's cockpit: Now / For / So far / runs / commits
    forge follow 57       # re-attach the loop to anything in flight
    forge preview 57      # boot (or reuse) the change's preview and print its address
    forge wait 57         # block until it ships, stops, or needs a person
                          # (a landing that ended — main moved under it — is
                          # named with why and the catch-up to do)

At an approval the loop shows the briefing and asks: approve, reject,
show the plan, spec or report the stage requires, or see the diff.
Rejecting takes a comment and dispatches the rework with your feedback
in the agent's prompt. With `-y` each approval is one line saying what it
agreed to; without a terminal and without `-y`, the loop stops and names
the exact `forge approve` or `forge follow -y` command, and the page.

Two estate commands sit beside the loop: `forge review [-p proj]` checks
every project (or one) against the ways of working it adopted and lists
what does not conform, and `forge delete <slug>` deletes a project after
you type its slug back, then follows its cleanup until it is done.

## Status

The Change lifecycle, the landing contract, the lane catalog, and the CLI
loop above are built and in daily use. [How changes reach main](/docs/guides/changes-and-main)
describes the branch, landing and approval rules, and
[Connect a coding agent or the CLI](/docs/guides/connect-a-coding-agent)
sets up the command line.
