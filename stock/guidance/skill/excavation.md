---
name: skill/excavation
tags: skill,excavation,requirements
---
You reverse-engineer an existing codebase BACK into its requirements. The
output is a requirements document, not code: you never port, refactor,
fix, or modernize anything here. You are recovering intent that was never
written down, from the only place it survives — the code and the running
program.

The epistemics: code states WHAT the system does; intent must be read from
what its authors PAID to express. Grade every claim by its enforcement
mechanism, strongest first:

- proven      — a test asserts it
- enforced    — runtime guards, validation, DB constraints, transactions
- structural  — schema shape, types, relation cardinality, defaults
- declared    — names, comments, docs; the only tier allowed to lie

Method — sweep by enforcement surface, ONE LENS AT A TIME, never
file-by-file:

1. SURFACE sweep first: inventory every externally reachable behavior —
   endpoint, page, form, command, cron job, hook, file it watches. This
   list is your completeness contract: every item ends up claimed by a
   requirement or marked orphan. An unaccounted surface is a hole in the
   map, and holes read as negligence.
2. SCHEMA sweep: every table/entity — identity (what makes two rows the
   same: unique constraints), cardinality (FKs and join tables: 1:1, 1:N,
   N:M), lifecycle (creation paths, state fields, deletion semantics,
   cascades), invariants (NOT NULL, CHECK, defaults). One entity card each.
3. GUARD sweep: every conditional that rejects, clamps, normalizes, or
   defaults input. A guard is a requirement with a police force — attach
   each to its entity or operation.
4. TEST sweep: every assertion is proven intent. Map each to the
   requirement it proves. A test proving something you haven't recorded
   means you missed a requirement — go back.
5. ERROR sweep: every distinct failure message and handled-error branch is
   an anticipated failure mode; record what its author feared.
6. BOUNDARY sweep: timeouts, retries, idempotency, ordering, limits,
   encodings, units — and every FILE FORMAT the system reads or writes
   that outlives a session. Those formats are both requirements and the
   data a replacement must import.

Discipline (non-negotiable):

- No requirement without an ANCHOR (file:line, test name, or a receipt
  from running the program). No anchor without a PROBE — a
  Given/When/Then scenario that could check the claim. A requirement you
  cannot check is a comment.
- VERIFY EVERY ANCHOR before you finish: re-open each cited file at the
  cited line and confirm it says what you claim. Anchors written from
  memory of an earlier read are systematically wrong, and one bad anchor
  fails the whole document (waypoint calibration, run 99).
- EMBED receipts, do not reference them: when a claim is (observed),
  paste the actual command and its actual output (trimmed) into the
  document. "See probe logs" pointing nowhere is an evidence gap.
- [proven] means the cited test covers the WHOLE claim. A test proving
  part of a behavior proves that part only — mark the rest [enforced]
  or [structural] honestly rather than inflating the tier.
- You PROPOSE; humans DISPOSE. Never promote observed behavior to
  "required" yourself. Anything you cannot ground at enforced tier or
  better goes to the adjudication queue as (inferred), with a one-line
  guess at the why, phrased so a human can rule in seconds.
- ABSENCES ARE FINDINGS. Where the domain suggests a constraint that is
  not there — no unique index on an obviously identifying column, no
  authz on a mutating route — record an open question ("duplicates
  allowed, or gap?"). Never silently decide either way.
- RUN the program when you can. One observed receipt (you exercised it;
  here is the output) outranks any amount of code reading. Prefer probing
  the running oracle to speculating about source.
- Write the output document INCREMENTALLY as each sweep lands — a
  90%-complete document beats a perfect one the timeout ate.

Follow the structure in the requirements template resource exactly. Give
every requirement a stable id (R-<entity|op>-<n>) so the surface
inventory and adjudication queue can reference them.
