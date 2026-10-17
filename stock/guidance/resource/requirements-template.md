---
name: resource/requirements-template
tags: resource,excavation,requirements
---
# Requirements: <system name>

Excavated from: <repo/ref, version, date> by <who/run>.

Legend — evidence tier: [proven] [enforced] [structural] [declared].
Status: (observed) exercised with receipt · (inferred) extractor guess
awaiting human ruling · (adjudicated: required|incidental|bug — by whom,
date). Every clause carries tier + anchor; probes are Given/When/Then.

## 1. System overview
Two paragraphs max: what it is, who used it for what. All [declared]
until adjudicated.

## 2. Entity cards
### <Entity>
- Identity: <what makes two the same> [tier] (anchor)
- Cardinality: <relations: 1:1 / 1:N / N:M> [tier] (anchor)
- Lifecycle: <born how / states / dies how, cascades> [tier] (anchor)
- Invariants: R-<entity>-1 <each guard or constraint> [tier] (anchor)
- Open questions: <noticed absences, one line each>

## 3. Operations
### <verb: endpoint / command / screen action>
R-<op>-1 — When <trigger>, the system shall <response>. [tier] (anchor)
- Preconditions: ... [tier] (anchor)
- Effects: <writes, transitions> [tier] (anchor)
- Failure modes: <each handled error → what the user sees> [tier] (anchor)
- Probe:
    Given <state>
    When <action>
    Then <checkable outcome>

## 4. Policies (cross-cutting)
Authz model, ordering guarantees, retention, limits, formats/units —
each with id, tier, anchor. This is the intent code-reading loses in the
noise; sweep results land here when they belong to no single entity.

## 5. Data formats to migrate
Every file/DB that outlives a session: location, format, sketch of
structure, what a replacement must import. These are requirements AND the
strongest probes — round-tripping real data exercises more invariants
than synthetic tests.

## 6. Capabilities (the why-layer)
Group operations and entities into user-meaningful capabilities, one line
each on why it exists. Written LAST, composed from the layers below.

## 7. Surface inventory (completeness contract)
Every reachable behavior from the surface sweep:
| surface | claimed by | note |
Each row: claimed by R-ids | ORPHAN (candidate dead code) | OUT OF SCOPE
(why). No blank cells.

## 8. Adjudication queue
Every (inferred) claim and open question, one per line, answerable in
seconds: "<behavior> — required, incidental, or bug?" (R-id)
