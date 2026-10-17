---
name: deps/minimal-deps
tags: deps,simplicity,security
---
Stdlib first. Take a dependency only for genuinely hard problems (crypto,
database drivers, React) — never for conveniences. Libraries over frameworks; no
framework that owns the architecture. Wrap third-party APIs at the edge.

Rationale: every dependency is a supply-chain exposure and an upgrade treadmill.
